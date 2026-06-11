package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGateCheckSatisfied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/gate/check" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "s")
	if _, err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"}); err != nil {
		t.Errorf("satisfied gate = %v, want nil", err)
	}
}

func TestGateCheckUnsatisfiedFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gate not satisfied", http.StatusConflict)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "s")
	if _, err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"}); err == nil {
		t.Error("409 must fail closed (error)")
	}
}

func TestGateCheckOfflineIsNoop(t *testing.T) {
	c := NewClient("", "")
	if _, err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"}); err != nil {
		t.Errorf("offline gate check = %v, want nil", err)
	}
}

// TestGateCheckReturnsRequester verifies that a 200 response with a JSON body
// {"requester": "poolA@x"} is decoded and returned as the first return value.
func TestGateCheckReturnsRequester(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"requester":"poolA@x"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "s")
	requester, err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"})
	if err != nil {
		t.Fatalf("GateCheck error = %v, want nil", err)
	}
	if requester != "poolA@x" {
		t.Errorf("requester = %q, want poolA@x", requester)
	}
}

// TestGateCheckOfflineReturnsEmptyRequester verifies that when no server is
// configured, GateCheck returns ("", nil) and makes no HTTP call.
func TestGateCheckOfflineReturnsEmptyRequester(t *testing.T) {
	// Point at a server that would fail the test if hit.
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Offline client: empty baseURL, NOT pointing at srv.
	c := NewClient("", "")
	requester, err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"})
	if err != nil {
		t.Errorf("offline GateCheck error = %v, want nil", err)
	}
	if requester != "" {
		t.Errorf("offline requester = %q, want empty", requester)
	}
	if hit {
		t.Error("offline GateCheck must not make an HTTP call")
	}
}

// TestGateCheckConflictReturnsEmptyRequester verifies that a 409 response
// returns ("", err) — requester is empty and error is non-nil.
func TestGateCheckConflictReturnsEmptyRequester(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gate not satisfied", http.StatusConflict)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "s")
	requester, err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"})
	if err == nil {
		t.Error("409 must return an error")
	}
	if requester != "" {
		t.Errorf("requester on 409 = %q, want empty", requester)
	}
}
