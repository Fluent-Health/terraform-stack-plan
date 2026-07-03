package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth/gauthtest"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// TestOIDCE2E drives the whole OIDC auth loop offline, with real cryptography
// on both sides and no shared secret configured (OIDC-only serve):
//
//	fabricated SA key (token_uri → fake JWT-bearer endpoint)
//	  → gauth.Source mints an ID token through the real client-library path
//	  → runner.Client attaches it
//	  → gauth.Verifier validates signature/expiry against the fake issuer's
//	    JWKS + audience allowlist
//	  → the middleware maps the verified email to scopes.
func TestOIDCE2E(t *testing.T) {
	const (
		audience    = "https://serve.e2e.test"
		runnerEmail = "e2e-runner@test.iam.gserviceaccount.com"
		viewerEmail = "e2e-viewer@test.example"
	)

	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	tokenSrv := issuer.TokenEndpoint()
	defer tokenSrv.Close()
	saPath, err := issuer.WriteServiceAccountKey(t.TempDir(), runnerEmail, tokenSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", saPath)

	// OIDC-only serve: no WebhookSecret — auth is enforced by the verifier.
	db, err := store.Open(filepath.Join(t.TempDir(), "oidc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := server.New(db, &server.MockGitHub{}, server.Config{
		APIPrincipals: map[string][]string{
			runnerEmail: {"report"},
			viewerEmail: {"read"},
		},
	})
	verify, err := gauth.Verifier(context.Background(), []string{audience}, issuer.ClientOption())
	if err != nil {
		t.Fatal(err)
	}
	app.APIVerifier = verify
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	ctx := context.Background()

	// Client side: the real library path — SA key file → JWT-bearer grant
	// against the fake token endpoint → issuer-signed ID token.
	src, err := gauth.Source(ctx, audience)
	if err != nil {
		t.Fatalf("gauth.Source over fabricated SA key: %v", err)
	}
	client := runner.NewClientTokenSource(srv.URL, src)

	// report scope: the full runner protocol slice must be authorized.
	if err := client.Init(ctx, events.Init{ID: "e2e-oidc-1", Repo: "o/r", Environment: "staging"}); err != nil {
		t.Fatalf("Init over OIDC: %v", err)
	}
	if err := client.Phase(ctx, events.PhaseEvent{ID: "e2e-oidc-1", Phase: events.PhaseWarming}); err != nil {
		t.Fatalf("Phase over OIDC: %v", err)
	}

	// A read-scope principal can fetch the execution but not report.
	get := func(tok, path string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	post := func(tok, path string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	viewerTok, err := issuer.MintIDToken(viewerEmail, audience, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := get(viewerTok, "/api/execution/e2e-oidc-1"); code != http.StatusOK {
		t.Errorf("read-scope GET execution = %d, want 200", code)
	}
	if code := post(viewerTok, "/api/init"); code != http.StatusForbidden {
		t.Errorf("read-scope POST /api/init = %d, want 403", code)
	}

	// Verified-but-unlisted identity → 403; wrong audience → 401; none → 401.
	strangerTok, _ := issuer.MintIDToken("stranger@test.example", audience, time.Hour)
	if code := post(strangerTok, "/api/init"); code != http.StatusForbidden {
		t.Errorf("unlisted identity = %d, want 403", code)
	}
	wrongAudTok, _ := issuer.MintIDToken(runnerEmail, "https://other.example", time.Hour)
	if code := post(wrongAudTok, "/api/init"); code != http.StatusUnauthorized {
		t.Errorf("wrong audience = %d, want 401", code)
	}
	if code := post("", "/api/init"); code != http.StatusUnauthorized {
		t.Errorf("no credentials = %d, want 401", code)
	}
}
