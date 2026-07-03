package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestStatusContext(t *testing.T) {
	if got := statusContext("staging"); got != "plan/staging" {
		t.Errorf("statusContext(staging) = %q", got)
	}
	if got := statusContext(""); got != "plan" {
		t.Errorf("statusContext(\"\") = %q", got)
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

func TestBearerAuth(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{WebhookSecret: "s3cret"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	wrongJWT, _ := jwtutil.Make("wrong", "runner", "api", time.Hour)
	viewJWT, _ := jwtutil.Make("s3cret", "runner", "view", time.Hour) // wrong aud for /api/*
	for _, h := range []string{"", "Bearer notajwt", "s3cret", "Bearer " + wrongJWT, "Bearer " + viewJWT} {
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

// TestAuthAccepted verifies that a valid HS256 JWT with aud=api is accepted.
func TestAuthAccepted(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{WebhookSecret: "s3cret"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	tok, err := jwtutil.Make("s3cret", "runner", "api", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("valid api JWT: got 401, want pass-through")
	}
}

func TestAuthDisabledWhenSecretEmpty(t *testing.T) {
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
		t.Fatalf("empty secret must disable auth, got 401")
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

// TestDualAcceptHS256WithOIDC verifies the migration posture: with both the
// shared secret and an OIDC verifier configured, legacy HS256 tokens keep full
// access and OIDC tokens work per scope.
func TestDualAcceptHS256WithOIDC(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{
		WebhookSecret: "s3cret",
		APIPrincipals: map[string][]string{"runner@x.iam.gserviceaccount.com": {"report"}},
	})
	a.APIVerifier = fakeOIDC(map[string]string{"tok-runner": "runner@x.iam.gserviceaccount.com"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	hs, _ := jwtutil.Make("s3cret", "runner", "api", time.Hour)
	for name, tok := range map[string]string{"hs256": hs, "oidc": "tok-runner"} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Errorf("%s token = %d, want pass-through", name, resp.StatusCode)
		}
	}

	// A wrong-secret HS256 token must not fall through to the OIDC verifier.
	bad, _ := jwtutil.Make("wrong", "runner", "api", time.Hour)
	req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	req.Header.Set("Authorization", "Bearer "+bad)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-secret hs256 = %d, want 401", resp.StatusCode)
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

func TestViewAuth(t *testing.T) {
	db := newServerTestDB(t)
	_ = store.UpsertInit(db, events.Init{
		ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}},
	})

	a := New(db, &MockGitHub{}, Config{WebhookSecret: "s3cret"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// No token → 401
	r1, err := http.Get(srv.URL + "/live/e1")
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusUnauthorized {
		t.Errorf("/live without token = %d, want 401", r1.StatusCode)
	}

	// Wrong-secret token → 401
	badTok, _ := jwtutil.Make("other", "viewer", "view", 30*24*time.Hour)
	r2, _ := http.Get(srv.URL + "/live/e1?token=" + badTok)
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Errorf("/live with wrong-secret token = %d, want 401", r2.StatusCode)
	}

	// Valid view token → 200 + session cookie
	tok, _ := jwtutil.Make("s3cret", "viewer", "view", 30*24*time.Hour)
	r3, err := http.Get(srv.URL + "/live/e1?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Errorf("/live with valid token = %d, want 200", r3.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range r3.Cookies() {
		if c.Name == "view_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Error("view_session cookie not set after ?token= access")
	}

	// Cookie path: subsequent request with cookie (no token) → 200
	jar := &singleCookieJar{cookie: cookie}
	client := &http.Client{Jar: jar}
	r4, err := client.Get(srv.URL + "/live/e1")
	if err != nil {
		t.Fatal(err)
	}
	r4.Body.Close()
	if r4.StatusCode != http.StatusOK {
		t.Errorf("/live with session cookie = %d, want 200", r4.StatusCode)
	}

	// /img is always public (GitHub camo fetches SVGs without auth)
	r5, _ := http.Get(srv.URL + "/img/e1.svg")
	r5.Body.Close()
	if r5.StatusCode == http.StatusUnauthorized {
		t.Error("/img must be public (no view auth)")
	}

	// No-secret config → all routes open
	a2 := New(db, &MockGitHub{}, Config{})
	srv2 := httptest.NewServer(a2.Routes())
	defer srv2.Close()
	r6, _ := http.Get(srv2.URL + "/live/e1")
	r6.Body.Close()
	if r6.StatusCode == http.StatusUnauthorized {
		t.Error("/live without secret config must be open")
	}
}

// singleCookieJar is a trivial http.CookieJar that holds one cookie for tests.
type singleCookieJar struct {
	cookie *http.Cookie
}

func (j *singleCookieJar) SetCookies(_ *url.URL, _ []*http.Cookie) {}
func (j *singleCookieJar) Cookies(_ *url.URL) []*http.Cookie {
	return []*http.Cookie{j.cookie}
}
