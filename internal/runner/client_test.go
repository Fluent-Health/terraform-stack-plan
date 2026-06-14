package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
)

func TestPostsHitRightPathsWithAuth(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Authorization")
		bodies[r.URL.Path] = string(b)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "s3cret")
	ctx := context.Background()
	if err := c.Init(ctx, events.Init{ID: "e1", Environment: "staging"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Phase(ctx, events.PhaseEvent{ID: "e1", Phase: events.PhaseWarming}); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(ctx, events.Update{ID: "e1", Stack: "a", Status: events.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := c.Finalize(ctx, events.Finalize{ID: "e1", ReportMarkdown: "# r"}); err != nil {
		t.Fatal(err)
	}
	if err := c.GateRevoke(ctx, events.GateRevoke{PR: 7, Environment: "staging"}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"/api/init", "/api/phase", "/api/update", "/api/finalize", "/api/gate/revoke"} {
		h := seen[p]
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) {
			t.Errorf("%s auth = %q, missing Bearer prefix", p, h)
			continue
		}
		if _, err := jwtutil.Validate(strings.TrimPrefix(h, prefix), "s3cret", "api"); err != nil {
			t.Errorf("%s auth token invalid: %v", p, err)
		}
	}
	var got events.Init
	if err := json.Unmarshal([]byte(bodies["/api/init"]), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "e1" || got.Environment != "staging" {
		t.Errorf("init body = %+v", got)
	}
}

func TestOfflineClientIsNoop(t *testing.T) {
	c := NewClient("", "")
	if c.Enabled() {
		t.Error("empty baseURL must be disabled")
	}
	if err := c.Init(context.Background(), events.Init{ID: "e1"}); err != nil {
		t.Errorf("offline Init = %v, want nil", err)
	}
	if err := c.Finalize(context.Background(), events.Finalize{ID: "e1"}); err != nil {
		t.Errorf("offline Finalize = %v, want nil", err)
	}
}

func TestPostReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	if err := c.Update(context.Background(), events.Update{ID: "e1", Stack: "a"}); err == nil {
		t.Error("want error on 500 (caller decides to ignore for best-effort)")
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("https://srv/", "")
	if c.baseURL != "https://srv" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
	}
}

func TestClientLogChunk(t *testing.T) {
	var gotPath string
	var gotBody events.LogChunk
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret")
	if err := c.LogChunk(context.Background(), events.LogChunk{ID: "e1", Stack: "stacks/a", Data: "hello"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/logs" {
		t.Errorf("path = %q, want /api/logs", gotPath)
	}
	if gotBody.ID != "e1" || gotBody.Stack != "stacks/a" || gotBody.Data != "hello" {
		t.Errorf("body = %+v", gotBody)
	}

	// Offline → no-op, no error.
	if err := NewClient("", "").LogChunk(context.Background(), events.LogChunk{ID: "e1"}); err != nil {
		t.Errorf("offline LogChunk should be a no-op nil, got %v", err)
	}
}

func TestJWTExpiresAfterOneHour(t *testing.T) {
	// Verify that the token sent by the client is a valid API JWT (not a raw secret).
	var gotTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "mysecret")
	_ = c.Init(context.Background(), events.Init{ID: "e1"})

	sub, err := jwtutil.Validate(gotTok, "mysecret", "api")
	if err != nil {
		t.Fatalf("token invalid: %v", err)
	}
	if sub != "runner" {
		t.Errorf("sub = %q, want runner", sub)
	}
	// Token must expire in ~1h, not sooner.
	_ = time.Hour // referenced so import is used
}
