package jwtutil

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	tok, err := Make("secret", "mysubject", "api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := Validate(tok, "secret", "api")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if sub != "mysubject" {
		t.Errorf("sub = %q, want mysubject", sub)
	}
}

func TestWrongSecret(t *testing.T) {
	tok, _ := Make("secret", "sub", "api", time.Hour)
	if _, err := Validate(tok, "other", "api"); err == nil {
		t.Error("wrong secret must fail")
	}
}

func TestWrongAud(t *testing.T) {
	tok, _ := Make("secret", "sub", "api", time.Hour)
	if _, err := Validate(tok, "secret", "view"); err == nil {
		t.Error("wrong aud must fail")
	}
}

func TestExpired(t *testing.T) {
	tok, _ := Make("secret", "sub", "api", -time.Second)
	if _, err := Validate(tok, "secret", "api"); err == nil {
		t.Error("expired token must fail")
	}
}

func TestMalformed(t *testing.T) {
	for _, bad := range []string{"", "a.b", "a.b.c.d", "notavalidbase64!!.b.c"} {
		if _, err := Validate(bad, "secret", "api"); err == nil {
			t.Errorf("malformed %q must fail", bad)
		}
	}
}

func TestEmptySecretMake(t *testing.T) {
	if _, err := Make("", "sub", "api", time.Hour); err == nil {
		t.Error("empty secret must fail")
	}
}

func TestAlg(t *testing.T) {
	tok, err := Make("secret", "runner", "api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := Alg(tok); got != "HS256" {
		t.Errorf("Alg(HS256 token) = %q", got)
	}
	// RS256 JOSE header (as on a Google-signed ID token).
	rs := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + ".payload.sig"
	if got := Alg(rs); got != "RS256" {
		t.Errorf("Alg(RS256 token) = %q", got)
	}
	if got := Alg("not-a-jwt"); got != "" {
		t.Errorf("Alg(garbage) = %q, want empty", got)
	}
}
