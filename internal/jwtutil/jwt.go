// Package jwtutil provides minimal HS256 JWT creation and validation for
// internal auth (API bearer tokens and view-link tokens). No external deps —
// uses only stdlib crypto/hmac, sha256, base64, and encoding/json.
package jwtutil

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// Make creates a signed HS256 JWT with the given subject, audience and TTL.
func Make(secret, sub, aud string, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", errors.New("jwtutil: empty secret")
	}
	hdr, _ := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}{"HS256", "JWT"})
	now := time.Now().Unix()
	pay, _ := json.Marshal(struct {
		Sub string `json:"sub"`
		Aud string `json:"aud"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}{sub, aud, now, now + int64(ttl.Seconds())})
	header := encode(hdr)
	payload := encode(pay)
	signing := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + encode(mac.Sum(nil)), nil
}

// Alg returns the alg field of the token's JOSE header, or "" when the header
// is not decodable. Used to route a bearer token to the matching verifier
// (HS256 shared-secret vs Google-signed OIDC) without trying both.
func Alg(token string) string {
	head, _, _ := strings.Cut(token, ".")
	hb, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return ""
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(hb, &h) != nil {
		return ""
	}
	return h.Alg
}

// Validate verifies the HS256 signature, expected audience, and expiry.
// Returns the sub claim on success.
func Validate(token, secret, expectedAud string) (string, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return "", errors.New("malformed token")
	}
	signing := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	if !hmac.Equal([]byte(parts[2]), []byte(encode(mac.Sum(nil)))) {
		return "", errors.New("invalid signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
		Aud any    `json:"aud"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(pb, &claims); err != nil {
		return "", fmt.Errorf("parse claims: %w", err)
	}
	var aud string
	switch v := claims.Aud.(type) {
	case string:
		aud = v
	case []any:
		if len(v) == 1 {
			aud, _ = v[0].(string)
		}
	}
	if aud != expectedAud {
		return "", fmt.Errorf("unexpected aud %q", aud)
	}
	if claims.Exp < time.Now().Unix() {
		return "", errors.New("token expired")
	}
	return claims.Sub, nil
}
