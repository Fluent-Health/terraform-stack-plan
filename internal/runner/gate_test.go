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
	if err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"}); err != nil {
		t.Errorf("satisfied gate = %v, want nil", err)
	}
}

func TestGateCheckUnsatisfiedFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gate not satisfied", http.StatusConflict)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "s")
	if err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"}); err == nil {
		t.Error("409 must fail closed (error)")
	}
}

func TestGateCheckOfflineIsNoop(t *testing.T) {
	c := NewClient("", "")
	if err := c.GateCheck(context.Background(), events.GateCheck{PR: 7, Environment: "staging"}); err != nil {
		t.Errorf("offline gate check = %v, want nil", err)
	}
}
