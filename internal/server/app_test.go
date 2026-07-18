package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestStatusContext(t *testing.T) {
	if got := statusContext("staging"); got != "plan/staging" {
		t.Errorf("statusContext(staging) = %q", got)
	}
	if got := statusContext(""); got != "plan" {
		t.Errorf("statusContext(\"\") = %q", got)
	}
}

func TestConsolidatedCheckNaming(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{Environment: "nonprod"})
	if got := a.planCheckName("nonprod"); got != "plan/nonprod" {
		t.Errorf("unarmed planCheckName = %q, want plan/nonprod", got)
	}
	if got := a.mergeGateCheckName("nonprod"); got != "apply-lock/nonprod" {
		t.Errorf("unarmed mergeGateCheckName = %q, want apply-lock/nonprod", got)
	}
	a.Executor = &fakeExecutor{}
	if got := a.planCheckName("nonprod"); got != "terraform/nonprod" {
		t.Errorf("armed planCheckName = %q, want terraform/nonprod", got)
	}
	if got := a.mergeGateCheckName("nonprod"); got != "terraform/nonprod" {
		t.Errorf("armed mergeGateCheckName = %q, want terraform/nonprod", got)
	}
}

func TestUIURL(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{UIBaseURL: "https://ui.fh.com", Environment: "nonprod"})
	// 1. With PR number -> repoints to /pr/{pr}
	if got := a.uiURL(7, "exec-123"); got != "https://ui.fh.com/pr/7" {
		t.Errorf("uiURL with PR = %q, want https://ui.fh.com/pr/7", got)
	}
	// 2. Without PR number -> falls back to deep route
	if got := a.uiURL(0, "exec-123"); got != "https://ui.fh.com/t/nonprod/e/exec-123" {
		t.Errorf("uiURL without PR = %q, want https://ui.fh.com/t/nonprod/e/exec-123", got)
	}
	// 3. No UI configured
	b := New(nil, &MockGitHub{}, Config{})
	if got := b.uiURL(7, "exec-123"); got != "" {
		t.Errorf("uiURL with no UI = %q, want empty string", got)
	}
}

func TestHealthz(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ready = %d, want 200", resp.StatusCode)
	}
}

// TestBearerAuth verifies that malformed/unrecognized bearer values —
// including an HS256 JWT shaped like the deleted legacy shared-secret
// credential — are rejected with 401 once an OIDC verifier is configured.
func TestBearerAuth(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	a.APIVerifier = fakeOIDC(map[string]string{"good-token": "runner@x.iam.gserviceaccount.com"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	legacyHS256 := "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJydW5uZXIiLCJhdWQiOiJhcGkifQ.3vJ0X1Zb6nYFhZ0m9m0v3wJ8vHc0d9XoJb5r8sQxYQU"
	for _, h := range []string{"", "Bearer notajwt", "s3cret", "Bearer wrong-token", legacyHS256} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q = %d, want 401", h, resp.StatusCode)
		}
	}
}

// TestAuthDisabledWithoutVerifier: with no APIVerifier configured (local/dev),
// /api/* auth is disabled entirely (local/dev).
func TestAuthDisabledWithoutVerifier(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("no APIVerifier configured must disable auth, got 401")
	}
}

// fakeOIDC returns an APIVerifier accepting exactly the given bearer values,
// mapped to emails.
func fakeOIDC(tokens map[string]string) func(ctx context.Context, bearer string) (string, error) {
	return func(_ context.Context, bearer string) (string, error) {
		if email, ok := tokens[bearer]; ok {
			return email, nil
		}
		return "", errAuth
	}
}

var errAuth = errors.New("bad token")

// TestOIDCAuthScopes exercises the OIDC path: verified identities get access
// per their configured scopes (want=0: any non-auth status), unknown
// identities and missing scopes are 403, unverifiable tokens are 401.
func TestOIDCAuthScopes(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{APIPrincipals: map[string][]string{
		"runner@x.iam.gserviceaccount.com": {"report"},
		"viewer@example.com":               {"read"},
		"ops@example.com":                  {"read", "admin"},
	}})
	a.APIVerifier = fakeOIDC(map[string]string{
		"tok-runner": "runner@x.iam.gserviceaccount.com",
		"tok-viewer": "viewer@example.com",
		"tok-ops":    "ops@example.com",
		"tok-nobody": "stranger@example.com",
	})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	cases := []struct {
		token, method, path string
		want                int // 0 = authorized: any status except 401/403
	}{
		{"tok-runner", "POST", "/api/init", 0},                              // report may report
		{"tok-runner", "POST", "/api/claims/release", 0},                    // report may release claims (runner post-apply cleanup; not ownership-checked)
		{"tok-runner", "POST", "/api/claims/list", 0},                       // report may list claims
		{"tok-runner", "GET", "/api/execution/nope", 0},                     // report may read
		{"tok-viewer", "POST", "/api/init", http.StatusForbidden},           // read cannot report
		{"tok-viewer", "POST", "/api/claims/release", http.StatusForbidden}, // read cannot release
		{"tok-viewer", "GET", "/api/execution/nope", 0},                     // read may read
		{"tok-ops", "POST", "/api/claims/release", 0},                       // admin may release
		{"tok-ops", "POST", "/api/init", http.StatusForbidden},              // admin is not the runner
		{"tok-nobody", "POST", "/api/init", http.StatusForbidden},           // verified but not allowlisted
		{"garbage", "POST", "/api/init", http.StatusUnauthorized},           // unverifiable token
		{"", "POST", "/api/init", http.StatusUnauthorized},                  // no token
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if c.want == 0 {
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want authorized pass-through", c.token, c.method, c.path, resp.StatusCode)
			}
		} else if resp.StatusCode != c.want {
			t.Errorf("%s %s %s = %d, want %d", c.token, c.method, c.path, resp.StatusCode, c.want)
		}
	}
}

// TestHS256RejectedOIDCAccepted verifies the post-HS256-deletion posture: a
// legacy HS256 token is rejected while a scoped OIDC token passes through.
func TestHS256RejectedOIDCAccepted(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{
		APIPrincipals: map[string][]string{"runner@x.iam.gserviceaccount.com": {"report"}},
	})
	a.APIVerifier = fakeOIDC(map[string]string{"tok-runner": "runner@x.iam.gserviceaccount.com"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// OIDC token → authorized pass-through.
	req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	req.Header.Set("Authorization", "Bearer tok-runner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("oidc token = %d, want pass-through", resp.StatusCode)
	}

	// A legacy HS256-shaped token (the deleted shared-secret credential) is
	// rejected like any unrecognized bearer — there is no secret-comparison
	// branch left on /api/*, and no signing secret configured at all.
	hs := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJydW5uZXIiLCJhdWQiOiJhcGkifQ.3vJ0X1Zb6nYFhZ0m9m0v3wJ8vHc0d9XoJb5r8sQxYQU"
	req2, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	req2.Header.Set("Authorization", "Bearer "+hs)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("legacy hs256 token = %d, want 401", resp2.StatusCode)
	}
}

// TestAuthActorInContext: handlers must see the verified OIDC identity via
// Actor (lowercased). A legacy HS256 bearer is rejected before the handler
// ever runs, so Actor stays unset for it.
func TestAuthActorInContext(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{
		APIPrincipals: map[string][]string{"runner@x.iam.gserviceaccount.com": {"report"}},
	})
	a.APIVerifier = fakeOIDC(map[string]string{"tok-runner": "Runner@x.iam.gserviceaccount.com"}) // mixed case → lowered
	var gotActor string
	h := a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActor = Actor(r)
	}), scopeReport)
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer tok-runner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotActor != "runner@x.iam.gserviceaccount.com" {
		t.Errorf("Actor = %q, want lowered OIDC email", gotActor)
	}

	// A legacy HS256 token is rejected before the handler runs — Actor stays unset.
	gotActor = ""
	hs := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJydW5uZXIiLCJhdWQiOiJhcGkifQ.3vJ0X1Zb6nYFhZ0m9m0v3wJ8vHc0d9XoJb5r8sQxYQU"
	req2, _ := http.NewRequest("POST", srv.URL, nil)
	req2.Header.Set("Authorization", "Bearer "+hs)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("legacy hs256 = %d, want 401", resp2.StatusCode)
	}
	if gotActor != "" {
		t.Errorf("Actor should stay unset for a rejected request, got %q", gotActor)
	}
}

// TestOIDCOnlyEnforcedWithoutSecret: an empty shared secret must NOT disable
// auth when an OIDC verifier is configured.
func TestOIDCOnlyEnforcedWithoutSecret(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	a.APIVerifier = fakeOIDC(map[string]string{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials with OIDC-only auth = %d, want 401", resp.StatusCode)
	}
}

func TestRetiredViewerRoutesGone(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	seedInit(t, a.shell, events.Init{
		ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}},
	})

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// The HTML viewer, its view-JWT machinery, and the /img DAG image all
	// retired with the central UI.
	for _, path := range []string{"/", "/live/e1", "/live/e1/events", "/pr/7", "/assets/app.css", "/img/e1.svg"} {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (viewer retired)", path, r.StatusCode)
		}
	}
}
