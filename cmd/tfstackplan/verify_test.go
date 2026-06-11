package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func TestRunVerifyRequiresDir(t *testing.T) {
	if code := runVerify([]string{}); code != 2 {
		t.Errorf("run verify with no --dir = %d, want 2", code)
	}
}

func TestRunVerifyE2E(t *testing.T) {
	dir := applyFixture(t)
	var mu sync.Mutex
	hits := map[string]int{}
	var gotInit events.Init
	var gotFinal events.Finalize
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/api/init":
			_ = json.Unmarshal(b, &gotInit)
		case "/api/finalize":
			_ = json.Unmarshal(b, &gotFinal)
		}
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv("TFSTACKPLAN_PR", "7")
	t.Setenv(runner.EnvExecution, "")

	if code := runVerify([]string{"--dir", dir, "--changed=false"}); code != 0 {
		t.Fatalf("run verify = %d, want 0", code)
	}
	for _, s := range []string{"stacks/a", "stacks/b"} {
		if _, err := os.Stat(filepath.Join(dir, s, "verified")); err != nil {
			t.Errorf("verify did not run for %s: %v", s, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/api/gate/check"] != 0 {
		t.Errorf("verify must not gate-check (hits=%d)", hits["/api/gate/check"])
	}
	if gotInit.Context != "verify/staging" {
		t.Errorf("init context = %q, want verify/staging", gotInit.Context)
	}
	if gotFinal.Failed {
		t.Error("finalize should not be Failed on a passing verify")
	}
	if hits["/api/logs"] == 0 {
		t.Error("expected per-stack logs to stream during verify")
	}
}
