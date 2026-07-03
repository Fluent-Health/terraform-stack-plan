// Package gauthtest provides a fake Google OIDC issuer for offline tests of
// the gauth mint/verify paths — no network, no credentials, no test users.
//
// Three pieces mirror the production topology:
//
//   - MintIDToken signs RS256 ID tokens with a throwaway key (the fake
//     "Google");
//   - ClientOption returns an HTTP client option whose transport serves this
//     issuer's JWKS, so gauth.Verifier validates those tokens for real
//     (idtoken checks signature/expiry/audience against the fetched keys and
//     does not pin the issuer);
//   - TokenEndpoint + WriteServiceAccountKey fake the OAuth2 JWT-bearer grant:
//     a fabricated service-account key whose token_uri points at the local
//     endpoint lets idtoken.NewTokenSource (via GOOGLE_APPLICATION_CREDENTIALS)
//     mint this issuer's tokens through the real client library path.
package gauthtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/api/option"
)

// Issuer is a fake Google OIDC token issuer backed by a throwaway RSA key.
type Issuer struct {
	Key   *rsa.PrivateKey
	KeyID string
}

// NewIssuer generates the issuer's signing key.
func NewIssuer() (*Issuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &Issuer{Key: key, KeyID: "gauthtest-key-1"}, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// jwks renders the issuer's public key in the certs shape idtoken expects.
func (i *Issuer) jwks() []byte {
	e := big.NewInt(int64(i.Key.PublicKey.E))
	doc, _ := json.Marshal(map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig",
			"kid": i.KeyID,
			"n":   b64(i.Key.PublicKey.N.Bytes()),
			"e":   b64(e.Bytes()),
		}},
	})
	return doc
}

// jwksTransport answers every request with the issuer's JWKS — the validator
// only ever fetches the (fixed) Google certs URL through the injected client.
type jwksTransport struct{ jwks []byte }

func (t jwksTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":  {"application/json"},
			"Cache-Control": {"max-age=3600"},
		},
		Body:    io.NopCloser(strings.NewReader(string(t.jwks))),
		Request: req,
	}, nil
}

// ClientOption returns the option to pass to gauth.Verifier so token
// validation fetches this issuer's JWKS instead of Google's.
func (i *Issuer) ClientOption() option.ClientOption {
	return option.WithHTTPClient(&http.Client{Transport: jwksTransport{jwks: i.jwks()}})
}

// MintIDToken signs an ID token for email/audience valid for ttl.
func (i *Issuer) MintIDToken(email, audience string, ttl time.Duration) (string, error) {
	now := time.Now()
	return i.MintIDTokenClaims(map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            audience,
		"sub":            "1234567890",
		"email":          email,
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(ttl).Unix(),
	})
}

// MintIDTokenClaims signs an ID token with exactly the given claims — edge
// cases (expired, unverified email, missing claims) set them explicitly.
func (i *Issuer) MintIDTokenClaims(claims map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": i.KeyID})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.Key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

// TokenEndpoint starts a fake OAuth2 token endpoint implementing the
// JWT-bearer grant the way Google's does for ID tokens: it decodes the
// (unverified) assertion, reads the service account's email from `iss` and
// the requested audience from `target_audience`, and answers with an
// id_token minted by this issuer. Close the returned server when done.
func (i *Issuer) TokenEndpoint() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		assertion := r.FormValue("assertion")
		parts := strings.Split(assertion, ".")
		if len(parts) != 3 {
			http.Error(w, "malformed assertion", http.StatusBadRequest)
			return
		}
		body, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			http.Error(w, "bad assertion payload", http.StatusBadRequest)
			return
		}
		var claims struct {
			Iss            string `json:"iss"`
			TargetAudience string `json:"target_audience"`
		}
		if err := json.Unmarshal(body, &claims); err != nil || claims.Iss == "" || claims.TargetAudience == "" {
			http.Error(w, "assertion missing iss/target_audience", http.StatusBadRequest)
			return
		}
		idTok, err := i.MintIDToken(claims.Iss, claims.TargetAudience, time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id_token":     idTok,
			"access_token": idTok,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
}

// WriteServiceAccountKey writes a fabricated service-account key file whose
// token_uri points at tokenURL, and returns its path. Point
// GOOGLE_APPLICATION_CREDENTIALS at it so idtoken.NewTokenSource mints through
// the real JWT-bearer flow against the fake endpoint.
func (i *Issuer) WriteServiceAccountKey(dir, email, tokenURL string) (string, error) {
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(i.Key),
	})
	doc, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "gauthtest",
		"private_key_id": i.KeyID,
		"private_key":    string(keyPEM),
		"client_email":   email,
		"client_id":      "000000000000000000000",
		"token_uri":      tokenURL,
	})
	path := filepath.Join(dir, "sa-"+strings.SplitN(email, "@", 2)[0]+".json")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
