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

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// TestClientTokenSource verifies the OIDC-path client attaches tokens from the
// injected source verbatim.
func TestClientTokenSource(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClientTokenSource(srv.URL, func(context.Context) (string, error) {
		return "id-token-123", nil
	})
	if err := c.Init(context.Background(), events.Init{ID: "e1"}); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer id-token-123" {
		t.Errorf("Authorization = %q, want Bearer id-token-123", got)
	}
}

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

	c := NewClientTokenSource(srv.URL, func(context.Context) (string, error) {
		return "tok-abc", nil
	})
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
		if got := seen[p]; got != "Bearer tok-abc" {
			t.Errorf("%s auth = %q, want Bearer tok-abc", p, got)
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
	c := NewClient("")
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
	c := NewClient(srv.URL)
	if err := c.Update(context.Background(), events.Update{ID: "e1", Stack: "a"}); err == nil {
		t.Error("want error on 500 (caller decides to ignore for best-effort)")
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("https://srv/")
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

	c := NewClient(srv.URL)
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
	if err := NewClient("").LogChunk(context.Background(), events.LogChunk{ID: "e1"}); err != nil {
		t.Errorf("offline LogChunk should be a no-op nil, got %v", err)
	}
}

func TestDecodeJWTSubject(t *testing.T) {
	// Header: {"alg":"none"} => eyJhbGciOiJub25lIn0
	// Payload: {"email":"user@example.com"} => eyJlbWFpbCI6InVzZXJAZXhhbXBsZS5jb20ifQ
	validToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6InVzZXJAZXhhbXBsZS5jb20ifQ."
	email, err := decodeJWTSubject(validToken)
	if err != nil {
		t.Fatalf("decodeJWTSubject failed: %v", err)
	}
	if email != "user@example.com" {
		t.Errorf("got email = %q, want user@example.com", email)
	}

	// Payload: {"sub":"sub-123"} => eyJzdWIiOiJzdWItMTIzIn0
	subToken := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJzdWItMTIzIn0."
	sub, err := decodeJWTSubject(subToken)
	if err != nil {
		t.Fatalf("decodeJWTSubject failed: %v", err)
	}
	if sub != "sub-123" {
		t.Errorf("got sub = %q, want sub-123", sub)
	}

	// Invalid format
	if _, err := decodeJWTSubject("invalid"); err == nil {
		t.Error("decodeJWTSubject did not fail on invalid token format")
	}
}

func TestClientEnrichedAuthErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	// Mock token function that returns a valid token payload we can decode
	mockToken := func(ctx context.Context) (string, error) {
		return "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6Im9wZXJhdG9yQGZsdWVudC1oZWFsdGguY29tIn0.", nil
	}

	c := NewClientTokenSource(srv.URL, mockToken)
	c.SetAudience("https://audience-example")

	err := c.Init(context.Background(), events.Init{ID: "e1"})
	if err == nil {
		t.Fatal("expected error on 401 response")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "Identity: operator@fluent-health.com") {
		t.Errorf("error string missing expected identity, got:\n%s", errStr)
	}
	if !strings.Contains(errStr, "Audience: https://audience-example") {
		t.Errorf("error string missing expected audience, got:\n%s", errStr)
	}
	if !strings.Contains(errStr, "api_admins") {
		t.Errorf("error string missing allowlist hint, got:\n%s", errStr)
	}
}
