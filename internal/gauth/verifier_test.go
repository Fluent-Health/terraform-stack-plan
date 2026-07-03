package gauth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth/gauthtest"
)

func newVerifier(t *testing.T, issuer *gauthtest.Issuer, audiences ...string) gauth.VerifyFunc {
	t.Helper()
	v, err := gauth.Verifier(context.Background(), audiences, issuer.ClientOption())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerifierAcceptsSignedToken(t *testing.T) {
	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	verify := newVerifier(t, issuer, "https://srv.example")
	tok, err := issuer.MintIDToken("runner@x.iam.gserviceaccount.com", "https://srv.example", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	email, err := verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if email != "runner@x.iam.gserviceaccount.com" {
		t.Errorf("email = %q", email)
	}
}

func TestVerifierAudienceAllowlist(t *testing.T) {
	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	verify := newVerifier(t, issuer, "https://srv.example", "gcloud-client-id.apps.googleusercontent.com")

	// Second allowlisted audience (the user-ADC shape) accepted.
	tok, _ := issuer.MintIDToken("ivan@example.com", "gcloud-client-id.apps.googleusercontent.com", time.Hour)
	if _, err := verify(context.Background(), tok); err != nil {
		t.Errorf("extra audience rejected: %v", err)
	}

	// Unlisted audience rejected even though the signature is valid.
	tok2, _ := issuer.MintIDToken("ivan@example.com", "https://other.example", time.Hour)
	if _, err := verify(context.Background(), tok2); err == nil || !strings.Contains(err.Error(), "unexpected audience") {
		t.Errorf("unlisted audience: err = %v, want unexpected-audience", err)
	}
}

func TestVerifierRejections(t *testing.T) {
	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	verify := newVerifier(t, issuer, "https://srv.example")
	now := time.Now()

	expired, _ := issuer.MintIDTokenClaims(map[string]any{
		"aud": "https://srv.example", "email": "a@b.c", "email_verified": true,
		"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix(),
	})
	noEmail, _ := issuer.MintIDTokenClaims(map[string]any{
		"aud": "https://srv.example", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	unverified, _ := issuer.MintIDTokenClaims(map[string]any{
		"aud": "https://srv.example", "email": "a@b.c", "email_verified": false,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})

	// A token signed by a DIFFERENT key must fail signature verification.
	other, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := other.MintIDToken("a@b.c", "https://srv.example", time.Hour)

	for name, tok := range map[string]string{
		"expired":          expired,
		"no-email":         noEmail,
		"unverified-email": unverified,
		"wrong-key":        forged,
		"garbage":          "not.a.jwt",
	} {
		if _, err := verify(context.Background(), tok); err == nil {
			t.Errorf("%s token accepted, want rejection", name)
		}
	}
}
