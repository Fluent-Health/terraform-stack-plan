package server

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub stands up an httptest server, points apiBase at it for the test,
// and returns it.
func fakeGitHub(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() {
		apiBase = old
		srv.Close()
	})
	return srv
}

func newTestRealClient(t *testing.T) *RealClient {
	t.Helper()
	k := testKey(t)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	c, err := NewRealClient("12345", "67890", pemKey)
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	return c
}

func TestNewRealClientValidates(t *testing.T) {
	k := testKey(t)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if _, err := NewRealClient("", "67890", pemKey); err == nil {
		t.Error("want error on empty app id")
	}
	if _, err := NewRealClient("12345", "", pemKey); err == nil {
		t.Error("want error on empty installation id")
	}
	if _, err := NewRealClient("12345", "67890", []byte("bad")); err == nil {
		t.Error("want error on bad key")
	}
}

func TestRealClientImplementsGitHub(t *testing.T) {
	var _ GitHub = (*RealClient)(nil)
}

func TestPRHeadSHAMintsTokenAndReads(t *testing.T) {
	var tokenHits, apiAuth int
	var sawBearerToken string
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/67890/access_tokens":
			tokenHits++
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("token request missing bearer JWT")
			}
			w.Write([]byte(`{"token":"ghs_test"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			apiAuth++
			sawBearerToken = r.Header.Get("Authorization")
			w.Write([]byte(`{"head":{"sha":"deadbeef"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	c := newTestRealClient(t)
	sha, err := c.PRHeadSHA(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "deadbeef" {
		t.Fatalf("sha = %q, want deadbeef", sha)
	}
	if tokenHits != 1 || apiAuth != 1 {
		t.Fatalf("tokenHits=%d apiAuth=%d, want 1/1", tokenHits, apiAuth)
	}
	if sawBearerToken != "Bearer ghs_test" {
		t.Fatalf("api call used %q, want the minted installation token", sawBearerToken)
	}
}

func TestSplitRepo(t *testing.T) {
	if _, _, err := splitRepo("noslash"); err == nil {
		t.Error("want error")
	}
	o, n, err := splitRepo("owner/name")
	if err != nil || o != "owner" || n != "name" {
		t.Fatalf("splitRepo = %q/%q/%v", o, n, err)
	}
}

func TestCreateCheckRun(t *testing.T) {
	var gotName, gotSHA, gotStatus, gotDetails string
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/67890/access_tokens" {
			w.Write([]byte(`{"token":"ghs_test"}`))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/repos/o/r/check-runs" {
			var raw map[string]any
			json.NewDecoder(r.Body).Decode(&raw)
			gotName, _ = raw["name"].(string)
			gotSHA, _ = raw["head_sha"].(string)
			gotStatus, _ = raw["status"].(string)
			gotDetails, _ = raw["details_url"].(string)
			w.Write([]byte(`{"id":555}`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	c := newTestRealClient(t)
	id, err := c.CreateCheckRun(context.Background(), "o/r", "sha123", "plan/staging", "https://srv/live/e1")
	if err != nil {
		t.Fatal(err)
	}
	if id != 555 {
		t.Fatalf("id = %d, want 555", id)
	}
	if gotName != "plan/staging" || gotSHA != "sha123" || gotStatus != "in_progress" || gotDetails != "https://srv/live/e1" {
		t.Fatalf("payload: name=%q sha=%q status=%q details=%q", gotName, gotSHA, gotStatus, gotDetails)
	}
}

func TestUpdateCheckRunTerminalSetsConclusion(t *testing.T) {
	var gotStatus, gotConclusion string
	var gotOutput map[string]any
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/67890/access_tokens" {
			w.Write([]byte(`{"token":"ghs_test"}`))
			return
		}
		if r.Method == "PATCH" && r.URL.Path == "/repos/o/r/check-runs/555" {
			var raw map[string]any
			json.NewDecoder(r.Body).Decode(&raw)
			gotStatus, _ = raw["status"].(string)
			gotConclusion, _ = raw["conclusion"].(string)
			gotOutput, _ = raw["output"].(map[string]any)
			w.Write([]byte(`{}`))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	c := newTestRealClient(t)
	err := c.UpdateCheckRun(context.Background(), "o/r", 555, CheckRunUpdate{
		Summary: "### progress", Text: "# report", DetailsURL: "u", Conclusion: "action_required",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotStatus != "completed" || gotConclusion != "action_required" {
		t.Fatalf("status=%q conclusion=%q, want completed/action_required", gotStatus, gotConclusion)
	}
	if s, _ := gotOutput["summary"].(string); s != "### progress" {
		t.Fatalf("output.summary = %q", s)
	}
}

func TestUpdateCheckRunRunningOmitsConclusion(t *testing.T) {
	var hadStatus, hadConclusion bool
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/67890/access_tokens" {
			w.Write([]byte(`{"token":"ghs_test"}`))
			return
		}
		var raw map[string]any
		json.NewDecoder(r.Body).Decode(&raw)
		_, hadStatus = raw["status"]
		_, hadConclusion = raw["conclusion"]
		w.Write([]byte(`{}`))
	})
	c := newTestRealClient(t)
	if err := c.UpdateCheckRun(context.Background(), "o/r", 555, CheckRunUpdate{Summary: "x"}); err != nil {
		t.Fatal(err)
	}
	if hadStatus || hadConclusion {
		t.Fatalf("running update must omit status/conclusion (status=%v conclusion=%v)", hadStatus, hadConclusion)
	}
}

func TestPostStatus(t *testing.T) {
	var raw map[string]any
	var path string
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/67890/access_tokens" {
			w.Write([]byte(`{"token":"ghs_test"}`))
			return
		}
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&raw)
		w.Write([]byte(`{}`))
	})
	c := newTestRealClient(t)
	err := c.PostStatus(context.Background(), "o/r", "sha123", "plan/staging", "pending", "planning 1/2 stacks", "https://srv/live/e1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repos/o/r/statuses/sha123" {
		t.Fatalf("path = %q", path)
	}
	if raw["state"] != "pending" || raw["context"] != "plan/staging" || raw["description"] != "planning 1/2 stacks" || raw["target_url"] != "https://srv/live/e1" {
		t.Fatalf("payload = %+v", raw)
	}
}

func TestDoPropagatesNon2xx(t *testing.T) {
	// Token mint succeeds, but the API call returns 500 — the error must propagate.
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/67890/access_tokens" {
			w.Write([]byte(`{"token":"ghs_test"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"boom"}`))
	})
	c := newTestRealClient(t)
	_, err := c.CreateCheckRun(context.Background(), "o/r", "sha", "staging", "")
	if err == nil {
		t.Fatal("expected error on 500 from GitHub")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention the status: %v", err)
	}
}

func TestMintTokenFailurePropagates(t *testing.T) {
	// The token exchange itself fails (401) — calls that need a token must fail.
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/67890/access_tokens" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"bad jwt"}`))
			return
		}
		t.Errorf("API must not be called when token mint fails: %s", r.URL.Path)
	})
	c := newTestRealClient(t)
	if err := c.PostStatus(context.Background(), "o/r", "sha", "plan/staging", "pending", "x", ""); err == nil {
		t.Fatal("expected error when token mint fails")
	}
}

func TestPRAbandoned(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"abandoned (closed, not merged)", `{"state":"closed","merged":false}`, true},
		{"merged (closed, merged)", `{"state":"closed","merged":true}`, false},
		{"open", `{"state":"open","merged":false}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/app/installations/67890/access_tokens":
					w.Write([]byte(`{"token":"ghs_test"}`))
				case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
					w.Write([]byte(tc.body))
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			})
			c := newTestRealClient(t)
			got, err := c.PRAbandoned(context.Background(), "o/r", 7)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("PRAbandoned(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
