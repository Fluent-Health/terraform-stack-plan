package ui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth/gauthtest"
)

const testClientID = "test-client.apps.googleusercontent.com"

func TestSessionCodec(t *testing.T) {
	c, err := newSessionCodec("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.seal(Session{Email: "a@example.com", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.open(tok)
	if err != nil || s.Email != "a@example.com" {
		t.Fatalf("round trip: %+v, %v", s, err)
	}
	// Expired sessions are rejected.
	tok2, _ := c.seal(Session{Email: "a@example.com", Expires: time.Now().Add(-time.Minute)})
	if _, err := c.open(tok2); err == nil {
		t.Error("expired session accepted")
	}
	// Tampering breaks the AEAD seal.
	if _, err := c.open(tok[:len(tok)-2] + "xx"); err == nil {
		t.Error("tampered token accepted")
	}
	// A different secret cannot open it.
	c2, _ := newSessionCodec("other")
	if _, err := c2.open(tok); err == nil {
		t.Error("foreign secret opened the session")
	}
}

// fakeGoogle wires a fake OAuth token endpoint + JWKS-backed id_token
// verifier: the code exchange returns an id_token with the given claims.
func fakeGoogle(t *testing.T, issuer *gauthtest.Issuer, claims map[string]any) (*oauth2.Config, gauth.VerifyClaimsFunc) {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idTok, err := issuer.MintIDTokenClaims(claims)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"id_token":%q}`, idTok)
	}))
	t.Cleanup(tokenSrv.Close)
	verify, err := gauth.ClaimsVerifier(context.Background(), []string{testClientID}, issuer.ClientOption())
	if err != nil {
		t.Fatal(err)
	}
	return &oauth2.Config{
		ClientID:     testClientID,
		ClientSecret: "shh",
		Endpoint:     oauth2.Endpoint{AuthURL: tokenSrv.URL + "/auth", TokenURL: tokenSrv.URL + "/token"},
		RedirectURL:  "https://ui.example.com/auth/callback",
		Scopes:       []string{"openid", "email", "profile"},
	}, verify
}

func idClaims(email, hd string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": "https://accounts.google.com", "aud": testClientID, "sub": "42",
		"email": email, "email_verified": true, "hd": hd,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// driveLogin runs the full browser flow against app and returns the session
// cookie from the callback (nil with the callback response when it failed).
func driveLogin(t *testing.T, h http.Handler) (*http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	// Step 1: /auth/login → state cookie + redirect carrying the same state.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/auth/login?next=/pr/7", nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	var state *http.Cookie
	var next *http.Cookie
	for _, c := range rr.Result().Cookies() {
		switch c.Name {
		case stateCookie:
			state = c
		case nextCookie:
			next = c
		}
	}
	if state == nil {
		t.Fatal("no state cookie set")
	}
	loc, _ := url.Parse(rr.Header().Get("Location"))
	if loc.Query().Get("state") != state.Value {
		t.Fatalf("redirect state %q != cookie %q", loc.Query().Get("state"), state.Value)
	}
	// Step 2: the callback with the provider's code. Success is a 200
	// interstitial carrying the session cookie (not a 302 — browsers may
	// refuse cookies set on the return-redirect hop).
	req := httptest.NewRequest("GET", "/auth/callback?code=c0de&state="+url.QueryEscape(state.Value), nil)
	req.AddCookie(state)
	if next != nil {
		req.AddCookie(next)
	}
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	for _, c := range rr2.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			return c, rr2
		}
	}
	return nil, rr2
}

func TestLoginFlow(t *testing.T) {
	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	oc, verify := fakeGoogle(t, issuer, idClaims("ivan@example.com", "example.com"))
	app, err := New(Config{
		SessionSecret: "s3cret",
		AllowedDomain: "example.com",
		OAuth:         oc,
		VerifyIDToken: verify,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := app.Routes()

	sess, rr := driveLogin(t, h)
	if sess == nil {
		t.Fatalf("no session cookie; callback: %d %s", rr.Code, rr.Body.String())
	}
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `location.replace("/pr/7")`) {
		t.Errorf("callback should land on the interstitial continuing to next: %d %q", rr.Code, rr.Body.String())
	}

	// The session opens /api/me with the verified identity.
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(sess)
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req)
	if rr3.Code != 200 || !strings.Contains(rr3.Body.String(), `"email":"ivan@example.com"`) {
		t.Errorf("/api/me: %d %s", rr3.Code, rr3.Body.String())
	}

	// No cookie → 401.
	rr4 := httptest.NewRecorder()
	h.ServeHTTP(rr4, httptest.NewRequest("GET", "/api/me", nil))
	if rr4.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /api/me: %d", rr4.Code)
	}

	// Callback with a wrong state → 400, no session.
	reqBad := httptest.NewRequest("GET", "/auth/callback?code=c&state=forged", nil)
	reqBad.AddCookie(&http.Cookie{Name: stateCookie, Value: "different"})
	rr5 := httptest.NewRecorder()
	h.ServeHTTP(rr5, reqBad)
	if rr5.Code != http.StatusBadRequest {
		t.Errorf("forged state: %d", rr5.Code)
	}
}

func TestLoginRejectsForeignWorkspace(t *testing.T) {
	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	oc, verify := fakeGoogle(t, issuer, idClaims("evil@other.com", "other.com"))
	app, err := New(Config{
		SessionSecret: "s3cret",
		AllowedDomain: "example.com",
		OAuth:         oc,
		VerifyIDToken: verify,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, rr := driveLogin(t, app.Routes())
	if sess != nil {
		t.Fatal("foreign-workspace account got a session")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestLoginRejectsMissingHD(t *testing.T) {
	issuer, err := gauthtest.NewIssuer()
	if err != nil {
		t.Fatal(err)
	}
	claims := idClaims("gmail-user@gmail.com", "")
	delete(claims, "hd") // consumer accounts carry no hd claim at all
	oc, verify := fakeGoogle(t, issuer, claims)
	app, err := New(Config{
		SessionSecret: "s3cret",
		AllowedDomain: "example.com",
		OAuth:         oc,
		VerifyIDToken: verify,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, rr := driveLogin(t, app.Routes())
	if sess != nil || rr.Code != http.StatusForbidden {
		t.Errorf("consumer account should be 403: session=%v code=%d", sess != nil, rr.Code)
	}
}

// newSessionApp builds an App with the given tiers and returns its handler
// plus a valid session cookie (bypassing the OAuth flow — covered above).
func newSessionApp(t *testing.T, tiers []Tier) (http.Handler, *http.Cookie) {
	t.Helper()
	app, err := New(Config{SessionSecret: "s3cret", Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := app.codec.seal(Session{Email: "ivan@example.com", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return app.Routes(), &http.Cookie{Name: SessionCookie, Value: sealed}
}

func get(t *testing.T, h http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestTierProxy(t *testing.T) {
	// A fake tier serve: asserts the S2S bearer arrives and answers the tier
	// contract's read routes, echoing the query it saw.
	var gotAuth, gotQuery string
	tierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/executions":
			fmt.Fprint(w, `[{"id":"e1"}]`)
		case r.URL.Path == "/api/approvals":
			fmt.Fprint(w, `[{"pr":7}]`)
		case r.URL.Path == "/api/pr/7":
			fmt.Fprint(w, `{"n":7}`)
		case strings.HasPrefix(r.URL.Path, "/api/execution/"):
			if strings.HasSuffix(r.URL.Path, "/nope") {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"ID":"e1"}`)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusTeapot)
		}
	}))
	defer tierSrv.Close()
	// A dead tier for the unreachable case.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	token := func(ctx context.Context) (string, error) { return "s2s-token", nil }
	h, sess := newSessionApp(t, []Tier{
		{Name: "nonprod", URL: tierSrv.URL, Token: token},
		{Name: "prod", URL: dead.URL, Token: token},
	})

	// Executions proxy: body passthrough, query passthrough, bearer attached.
	rr := get(t, h, "/api/tiers/nonprod/executions?pr=7&limit=5", sess)
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `[{"id":"e1"}]` {
		t.Errorf("executions proxy: %d %s", rr.Code, rr.Body.String())
	}
	if gotAuth != "Bearer s2s-token" {
		t.Errorf("tier call auth: %q", gotAuth)
	}
	if !strings.Contains(gotQuery, "pr=7") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query not passed through: %q", gotQuery)
	}

	// Approvals + execution detail proxies.
	if rr := get(t, h, "/api/tiers/nonprod/approvals", sess); rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `[{"pr":7}]` {
		t.Errorf("approvals proxy: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get(t, h, "/api/tiers/nonprod/executions/e1", sess); rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"ID":"e1"}` {
		t.Errorf("execution proxy: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get(t, h, "/api/tiers/nonprod/pr/7", sess); rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != `{"n":7}` {
		t.Errorf("PR proxy: %d %s", rr.Code, rr.Body.String())
	}
	// Tier-side non-2xx statuses pass through untouched.
	if rr := get(t, h, "/api/tiers/nonprod/executions/nope", sess); rr.Code != http.StatusNotFound {
		t.Errorf("tier 404 should pass through: %d", rr.Code)
	}

	// The tiers listing reflects config order.
	rr = get(t, h, "/api/tiers", sess)
	var tiers []struct{ Name string }
	if err := json.Unmarshal(rr.Body.Bytes(), &tiers); err != nil || len(tiers) != 2 || tiers[0].Name != "nonprod" {
		t.Errorf("/api/tiers: %d %s (%v)", rr.Code, rr.Body.String(), err)
	}

	// A dead tier is a 502 naming the tier; others unaffected.
	if rr := get(t, h, "/api/tiers/prod/executions", sess); rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "prod unreachable") {
		t.Errorf("dead tier: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get(t, h, "/api/tiers/prod/pr/7", sess); rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "prod unreachable") {
		t.Errorf("dead tier PR proxy: %d %s", rr.Code, rr.Body.String())
	}
	// Unknown tier → 404.
	if rr := get(t, h, "/api/tiers/stage/executions", sess); rr.Code != http.StatusNotFound {
		t.Errorf("unknown tier: %d", rr.Code)
	}
	// No session → 401 without touching the tier.
	if rr := get(t, h, "/api/tiers/nonprod/executions", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated proxy: %d", rr.Code)
	}
}

func TestPublicSurfaceAndSPA(t *testing.T) {
	h, sess := newSessionApp(t, nil)
	if rr := get(t, h, "/healthz", nil); rr.Code != 200 {
		t.Errorf("/healthz should be public: %d", rr.Code)
	}
	// The SPA shell serves at / and at client-routed paths, without a session
	// (the SPA itself redirects to login when /api/me 401s).
	for _, p := range []string{"/", "/pr/7", "/tiers/nonprod/executions/e1"} {
		rr := get(t, h, p, nil)
		body, _ := io.ReadAll(rr.Body)
		if rr.Code != 200 || !strings.Contains(string(body), "tfstackplan ui") {
			t.Errorf("SPA fallback %s: %d", p, rr.Code)
		}
	}
	// Logout clears the session cookie.
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(sess)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rr.Code)
	}
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
}

func TestStreamProxies(t *testing.T) {
	var gotAuth, gotLastID, gotQuery string
	tierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotLastID = r.Header.Get("Last-Event-ID")
		gotQuery = r.URL.RawQuery
		switch {
		case r.URL.Path == "/api/execution/e1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprint(w, "data: changed\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			fmt.Fprint(w, "event: superseded\ndata: e2\n\n")
		case strings.HasPrefix(r.URL.Path, "/logs/e1/"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: 42\ndata: terraform says hi\n\n")
		case strings.HasPrefix(r.URL.Path, "/plan/e1/"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<h3>stacks/a</h3>")
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusTeapot)
		}
	}))
	defer tierSrv.Close()

	token := func(ctx context.Context) (string, error) { return "s2s-token", nil }
	h, sess := newSessionApp(t, []Tier{{Name: "nonprod", URL: tierSrv.URL, Token: token}})

	// SSE events proxy: bearer attached, both events relayed, headers kept.
	rr := get(t, h, "/api/tiers/nonprod/executions/e1/events", sess)
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events proxy: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if body := rr.Body.String(); !strings.Contains(body, "data: changed") || !strings.Contains(body, "event: superseded") {
		t.Errorf("events body: %q", body)
	}
	if gotAuth != "Bearer s2s-token" {
		t.Errorf("events auth: %q", gotAuth)
	}

	// Log proxy: follow + Last-Event-ID forwarded for resume.
	req := httptest.NewRequest("GET", "/api/tiers/nonprod/logs/e1/stacks/a?follow=1", nil)
	req.Header.Set("Last-Event-ID", "17")
	req.AddCookie(sess)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != 200 || !strings.Contains(rr2.Body.String(), "terraform says hi") {
		t.Errorf("log proxy: %d %s", rr2.Code, rr2.Body.String())
	}
	if gotLastID != "17" || gotQuery != "follow=1" {
		t.Errorf("log resume passthrough: last-id=%q query=%q", gotLastID, gotQuery)
	}

	// Plan fragment proxy.
	if rr := get(t, h, "/api/tiers/nonprod/plan/e1/stacks/a", sess); rr.Code != 200 || !strings.Contains(rr.Body.String(), "<h3>stacks/a</h3>") {
		t.Errorf("plan proxy: %d %s", rr.Code, rr.Body.String())
	}

	// Session + tier guards hold on the stream routes too.
	if rr := get(t, h, "/api/tiers/nonprod/executions/e1/events", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("events without session: %d", rr.Code)
	}
	if rr := get(t, h, "/api/tiers/nope/logs/e1/stacks/a", sess); rr.Code != http.StatusNotFound {
		t.Errorf("unknown tier stream: %d", rr.Code)
	}
}

func signBody(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newRelayApp(t *testing.T, tiers []Tier) http.Handler {
	t.Helper()
	app, err := New(Config{SessionSecret: "s3cret", GitHubWebhookSecret: "gh-app-secret", Tiers: tiers})
	if err != nil {
		t.Fatal(err)
	}
	return app.Routes()
}

func TestGitHubRelay(t *testing.T) {
	type seen struct {
		body   string
		event  string
		bearer string
		sig    string
	}
	var mu sync.Mutex
	got := map[string]seen{}
	tierHandler := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/github/webhook" {
				http.Error(w, "wrong path "+r.URL.Path, http.StatusTeapot)
				return
			}
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			got[name] = seen{
				body:   string(b),
				event:  r.Header.Get("X-GitHub-Event"),
				bearer: r.Header.Get("Authorization"),
				sig:    r.Header.Get("X-Hub-Signature-256"),
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}
	}
	t1 := httptest.NewServer(tierHandler("nonprod"))
	defer t1.Close()
	t2 := httptest.NewServer(tierHandler("prod"))
	defer t2.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	h := newRelayApp(t, []Tier{{Name: "nonprod", URL: t1.URL}, {Name: "prod", URL: t2.URL}})

	body := `{"action":"rerequested"}`
	sig := signBody("gh-app-secret", body)
	req := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "check_run")
	req.Header.Set("X-Hub-Signature-256", sig)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("relay = %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	for _, name := range []string{"nonprod", "prod"} {
		s, ok := got[name]
		if !ok || s.body != body || s.event != "check_run" {
			t.Errorf("tier %s got %+v", name, s)
		}
		// Verbatim pipe: GitHub's signature travels through so each serve
		// verifies authenticity END-TO-END; no relay-minted credentials.
		if s.sig != sig || s.bearer != "" {
			t.Errorf("tier %s auth passthrough: bearer=%q sig=%q", name, s.bearer, s.sig)
		}
	}
	mu.Unlock()

	// Defense in depth: with a configured secret, a bad GitHub signature is
	// rejected at the relay and nothing is forwarded.
	mu.Lock()
	got = map[string]seen{}
	mu.Unlock()
	reqBad := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
	reqBad.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rrBad := httptest.NewRecorder()
	h.ServeHTTP(rrBad, reqBad)
	mu.Lock()
	forwarded := len(got)
	mu.Unlock()
	if rrBad.Code != http.StatusUnauthorized || forwarded != 0 {
		t.Errorf("bad signature: code=%d forwarded=%d", rrBad.Code, forwarded)
	}

	// Without a configured secret the relay is a pure pipe: it forwards
	// blindly (the serves reject bad signatures themselves).
	hOff, _ := newSessionApp(t, []Tier{{Name: "nonprod", URL: t1.URL}})
	mu.Lock()
	got = map[string]seen{}
	mu.Unlock()
	reqBlind := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
	reqBlind.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rrBlind := httptest.NewRecorder()
	hOff.ServeHTTP(rrBlind, reqBlind)
	mu.Lock()
	blindForwarded := len(got)
	mu.Unlock()
	if rrBlind.Code != http.StatusAccepted || blindForwarded != 1 {
		t.Errorf("secretless relay should forward blindly: code=%d forwarded=%d", rrBlind.Code, blindForwarded)
	}

	// One tier down still 202 (the other accepted); all down → 502.
	h2 := newRelayApp(t, []Tier{{Name: "up", URL: t1.URL}, {Name: "down", URL: dead.URL}})
	req2 := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
	req2.Header.Set("X-Hub-Signature-256", signBody("gh-app-secret", body))
	rr2 := httptest.NewRecorder()
	h2.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusAccepted {
		t.Errorf("one tier down: %d", rr2.Code)
	}
	h3 := newRelayApp(t, []Tier{{Name: "down", URL: dead.URL}})
	req3 := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
	req3.Header.Set("X-Hub-Signature-256", signBody("gh-app-secret", body))
	rr3 := httptest.NewRecorder()
	h3.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusBadGateway {
		t.Errorf("all tiers down: %d", rr3.Code)
	}
}
