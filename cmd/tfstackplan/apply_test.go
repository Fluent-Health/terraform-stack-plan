package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func TestRunApplyRequiresDir(t *testing.T) {
	if code := runApply([]string{}); code != 2 {
		t.Errorf("run apply with no --dir = %d, want 2", code)
	}
}

func TestRunApplyFailsClosedOnUnsatisfiedGate(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/api/gate/check" {
			http.Error(w, "gate not satisfied", http.StatusConflict)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")

	if code := runApply([]string{"--dir", t.TempDir()}); code != 1 {
		t.Fatalf("run apply on unsatisfied gate = %d, want 1 (fail closed)", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/api/gate/check"] != 1 {
		t.Errorf("gate check hits = %d, want 1", hits["/api/gate/check"])
	}
	if hits["/api/init"] != 0 {
		t.Errorf("init hit %d times; apply must not start when the gate is unsatisfied", hits["/api/init"])
	}
}
