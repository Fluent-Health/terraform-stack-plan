package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// parseRSAPrivateKey accepts a PEM-encoded RSA key in PKCS#1 or PKCS#8 form
// (GitHub Apps issue a PKCS#1 ".pem", but PKCS#8 is also a legal encoding).
func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("github: could not decode PEM private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: parse key as PKCS1 or PKCS8: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: PKCS8 key is not RSA")
	}
	return rsaKey, nil
}

// appJWT mints a GitHub App JWT (RS256), signed with the App private key. GitHub
// caps the lifetime at 10 minutes; we use ~9 and backdate iat 60s to tolerate
// clock skew. Hand-rolled (header.claims.signature) to avoid a JWT dependency.
func appJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"RS256","typ":"JWT"}`))
	iat := now.Add(-60 * time.Second).Unix()
	claims := enc([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%q}`, iat, iat+540, appID)))
	signingInput := header + "." + claims
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign app jwt: %w", err)
	}
	return signingInput + "." + enc(sig), nil
}
