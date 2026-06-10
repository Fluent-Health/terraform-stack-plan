package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestParseRSAPrivateKeyPKCS1AndPKCS8(t *testing.T) {
	k := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if _, err := parseRSAPrivateKey(pkcs1); err != nil {
		t.Errorf("PKCS1: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := parseRSAPrivateKey(pkcs8); err != nil {
		t.Errorf("PKCS8: %v", err)
	}
	if _, err := parseRSAPrivateKey([]byte("not a pem")); err == nil {
		t.Error("expected error on garbage PEM")
	}
}

func TestAppJWTStructureAndSignature(t *testing.T) {
	k := testKey(t)
	now := time.Unix(1_700_000_000, 0)
	tok, err := appJWT("12345", k, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var h struct{ Alg, Typ string }
	if err := json.Unmarshal(hdr, &h); err != nil {
		t.Fatal(err)
	}
	if h.Alg != "RS256" || h.Typ != "JWT" {
		t.Errorf("header = %+v", h)
	}
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if c.Iss != "12345" {
		t.Errorf("iss = %q", c.Iss)
	}
	if c.Iat != now.Add(-60*time.Second).Unix() {
		t.Errorf("iat = %d, want backdated 60s", c.Iat)
	}
	if c.Exp-c.Iat != 540 {
		t.Errorf("exp-iat = %d, want 540", c.Exp-c.Iat)
	}
	signingInput := parts[0] + "." + parts[1]
	sum := sha256.Sum256([]byte(signingInput))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature verify: %v", err)
	}
}
