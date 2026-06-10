package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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
		if seen[p] != "Bearer s3cret" {
			t.Errorf("%s auth = %q, want Bearer s3cret", p, seen[p])
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
