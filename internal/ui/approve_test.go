package ui

import (
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
)

// approveTestApp wires an App with a fake Google token endpoint, a fake PAM,
// and a fake tier serve (recording reconcile nudges), returning the handler,
// a session cookie, and the recorders.
func approveTestApp(t *testing.T) (http.Handler, *http.Cookie, *pamRecorder, *tierRecorder) {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"user-cloud-token","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)
	pam := &pamRecorder{status: 200}
	pamSrv := httptest.NewServer(pam)
	t.Cleanup(pamSrv.Close)
	tier := &tierRecorder{}
	tierSrv := httptest.NewServer(tier)
	t.Cleanup(tierSrv.Close)

	app, err := New(Config{
		PublicBaseURL: "https://ui.example.com",
		SessionSecret: "s3cret",
		AllowedDomain: "example.com",
		OAuth: &oauth2.Config{
			ClientID:     testClientID,
			ClientSecret: "shh",
			Endpoint:     oauth2.Endpoint{AuthURL: tokenSrv.URL + "/auth", TokenURL: tokenSrv.URL + "/token"},
			RedirectURL:  "https://ui.example.com/auth/callback",
			Scopes:       []string{"openid", "email", "profile"},
		},
		QuotaProject: "svc-proj",
		PAMBaseURL:   pamSrv.URL,
		Tiers:        []Tier{{Name: "prod", URL: tierSrv.URL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := app.codec.seal(Session{Email: "ivan@example.com", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return app.Routes(), &http.Cookie{Name: SessionCookie, Value: sealed}, pam, tier
}

// tierRecorder records requests the UI backend makes to the tier serve (the
// post-decision gate-reconcile nudge).
type tierRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (tr *tierRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tr.mu.Lock()
	tr.paths = append(tr.paths, r.Method+" "+r.URL.Path)
	tr.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

// waitFor polls until the recorder has seen a path or the deadline passes.
func (tr *tierRecorder) waitFor(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		for _, p := range tr.paths {
			if p == path {
				tr.mu.Unlock()
				return true
			}
		}
		tr.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

type pamRecorder struct {
	status int
	body   string
	path   string
	auth   string
	quota  string
	reason string
	calls  int
}

func (p *pamRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.calls++
	p.path = r.URL.Path
	p.auth = r.Header.Get("Authorization")
	p.quota = r.Header.Get("x-goog-user-project")
	b, _ := io.ReadAll(r.Body)
	p.reason = string(b)
	w.WriteHeader(p.status)
	io.WriteString(w, p.body)
}

const approveGrant = "projects/proj-1/locations/global/entitlements/iam/grants/g1"

func startApprove(t *testing.T, h http.Handler, sess *http.Cookie, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/auth/approve?"+query, nil)
	req.AddCookie(sess)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestApproveFlow(t *testing.T) {
	h, sess, pam, tier := approveTestApp(t)

	// Step 1: start → redirect to Google with cloud-platform scope,
	// incremental consent, and the sealed intent as state.
	rr := startApprove(t, h, sess, "tier=prod&decision=approve&reason=lgtm&grant="+url.QueryEscape(approveGrant))
	if rr.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	loc, _ := url.Parse(rr.Header().Get("Location"))
	q := loc.Query()
	if q.Get("scope") != cloudPlatformScope || q.Get("include_granted_scopes") != "true" {
		t.Errorf("consent params: scope=%q inc=%q", q.Get("scope"), q.Get("include_granted_scopes"))
	}
	if got := q.Get("redirect_uri"); got != "https://ui.example.com/auth/approve/callback" {
		t.Errorf("redirect_uri: %q", got)
	}
	state := q.Get("state")

	// Step 2: callback → code exchange → PAM :approve as the user token.
	req := httptest.NewRequest("GET", "/auth/approve/callback?code=c0de&state="+url.QueryEscape(state), nil)
	req.AddCookie(sess)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != 200 || !strings.Contains(rr2.Body.String(), `"ok":true`) {
		t.Fatalf("callback: %d %s", rr2.Code, rr2.Body.String())
	}
	if pam.calls != 1 || pam.path != "/"+approveGrant+":approve" {
		t.Errorf("pam call: calls=%d path=%q", pam.calls, pam.path)
	}
	if pam.auth != "Bearer user-cloud-token" || pam.quota != "svc-proj" {
		t.Errorf("pam identity: auth=%q quota=%q", pam.auth, pam.quota)
	}
	if !strings.Contains(pam.reason, "lgtm") {
		t.Errorf("pam reason: %q", pam.reason)
	}
	if !strings.Contains(rr2.Body.String(), "postMessage") {
		t.Errorf("popup should postMessage the outcome: %s", rr2.Body.String())
	}
	// Step 3: a successful decision nudges the tier's gate reconcile so the
	// outcome pushes to watching pages via SSE (no reload).
	if !tier.waitFor("POST /api/gate/reconcile", 2*time.Second) {
		t.Errorf("expected a gate-reconcile nudge on the tier; saw %v", tier.paths)
	}
}

// PAM requires a reason — an empty submit must carry a default, never an
// empty body that PAM rejects.
func TestApproveEmptyReasonDefaults(t *testing.T) {
	h, sess, pam, _ := approveTestApp(t)
	rr := startApprove(t, h, sess, "tier=prod&decision=approve&grant="+url.QueryEscape(approveGrant))
	state := mustState(t, rr)
	req := httptest.NewRequest("GET", "/auth/approve/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(sess)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if !strings.Contains(rr2.Body.String(), `"ok":true`) {
		t.Fatalf("callback: %s", rr2.Body.String())
	}
	if !strings.Contains(pam.reason, "approved via tfstackplan UI") {
		t.Errorf("empty reason must default; pam got %q", pam.reason)
	}
}

func TestApprovePAM403Verbatim(t *testing.T) {
	h, sess, pam, tier := approveTestApp(t)
	pam.status = 403
	pam.body = `{"error":{"message":"Permission 'privilegedaccessmanager.grants.approve' denied"}}`
	rr := startApprove(t, h, sess, "tier=prod&decision=deny&grant="+url.QueryEscape(approveGrant))
	state := mustState(t, rr)
	req := httptest.NewRequest("GET", "/auth/approve/callback?code=c&state="+url.QueryEscape(state), nil)
	req.AddCookie(sess)
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	body := rr2.Body.String()
	if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "privilegedaccessmanager.grants.approve") {
		t.Errorf("PAM 403 should surface verbatim: %s", body)
	}
	if !strings.HasSuffix(pam.path, ":deny") {
		t.Errorf("deny path: %q", pam.path)
	}
	// A failed decision must NOT nudge the tier — nothing changed.
	if tier.waitFor("POST /api/gate/reconcile", 200*time.Millisecond) {
		t.Errorf("no nudge expected after a PAM rejection; saw %v", tier.paths)
	}
}

func TestApproveGuards(t *testing.T) {
	h, sess, pam, _ := approveTestApp(t)

	// Start validation.
	if rr := startApprove(t, h, sess, "tier=nope&decision=approve&grant="+url.QueryEscape(approveGrant)); rr.Code != http.StatusNotFound {
		t.Errorf("unknown tier: %d", rr.Code)
	}
	if rr := startApprove(t, h, sess, "tier=prod&decision=maybe&grant="+url.QueryEscape(approveGrant)); rr.Code != http.StatusBadRequest {
		t.Errorf("bad decision: %d", rr.Code)
	}
	if rr := startApprove(t, h, sess, "tier=prod&decision=approve&grant=not-a-grant"); rr.Code != http.StatusBadRequest {
		t.Errorf("bad grant name: %d", rr.Code)
	}
	// No session → 401 before anything happens.
	req := httptest.NewRequest("GET", "/auth/approve?tier=prod&decision=approve&grant="+url.QueryEscape(approveGrant), nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("start without session: %d", rr.Code)
	}

	// Callback guards: tampered state, foreign session, user cancel — none
	// may reach PAM.
	valid := mustState(t, startApprove(t, h, sess, "tier=prod&decision=approve&grant="+url.QueryEscape(approveGrant)))

	// A different user's session must not be able to finish this intent.
	foreignApp, _ := New(Config{SessionSecret: "s3cret", Tiers: []Tier{{Name: "prod", URL: "http://tier.invalid"}}})
	foreignSealed, _ := foreignApp.codec.seal(Session{Email: "other@example.com", Expires: time.Now().Add(time.Hour)})
	foreign := &http.Cookie{Name: SessionCookie, Value: foreignSealed}

	cases := []struct {
		name string
		url  string
		sess *http.Cookie
		want string
	}{
		{"tampered state", "/auth/approve/callback?code=c&state=bogus", sess, "invalid"},
		{"user cancelled", "/auth/approve/callback?error=access_denied&state=" + url.QueryEscape(valid), sess, "consent not granted"},
		{"foreign session", "/auth/approve/callback?code=c&state=" + url.QueryEscape(valid), foreign, "different session"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.url, nil)
			req.AddCookie(c.sess)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if !strings.Contains(rr.Body.String(), c.want) {
				t.Errorf("want %q in %s", c.want, rr.Body.String())
			}
		})
	}
	if pam.calls != 0 {
		t.Errorf("no guard case may reach PAM; calls=%d", pam.calls)
	}
}

func mustState(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	if rr.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	loc, err := url.Parse(rr.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query().Get("state")
}
