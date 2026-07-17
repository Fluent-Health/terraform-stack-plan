package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestDispatchClaimsList(t *testing.T) {
	// Mock server that returns a mock list of claims
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/claims/list" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Decode request body
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["environment"] != "prod" {
			http.Error(w, "bad env", http.StatusBadRequest)
			return
		}

		claims := []events.Claim{
			{
				Environment: "prod",
				StackPath:   "stacks/demo",
				OwnerPR:     7,
				ExpiresAt:   time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(claims)
	}))
	defer srv.Close()

	os.Setenv("TFSTACKPLAN_SERVER", srv.URL)
	os.Setenv("TFSTACKPLAN_TOKEN", "dummy-token")
	defer func() {
		os.Unsetenv("TFSTACKPLAN_SERVER")
		os.Unsetenv("TFSTACKPLAN_TOKEN")
	}()

	// 1. Valid dispatch with claims
	if code := dispatch([]string{"claims", "list", "--env", "prod"}); code != 0 {
		t.Fatalf("claims list exit = %d, want 0", code)
	}

	// 2. Offline / server unset scenario (should return 1 under Proposal 3)
	os.Unsetenv("TFSTACKPLAN_SERVER")
	if code := dispatch([]string{"claims", "list", "--env", "prod"}); code != 1 {
		t.Fatalf("claims list offline exit = %d, want 1", code)
	}
	os.Setenv("TFSTACKPLAN_SERVER", srv.URL)

	// 3. Optional --env behavior (Proposal 4)
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "stacks", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "stacks", "staging"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmpDir)

	// When TFSTACKPLAN_SERVER is set, dispatch with no --env should try to fetch
	// prod and staging. It should exit 1 because staging fails on the mock server.
	if code := dispatch([]string{"claims", "list"}); code != 1 {
		t.Fatalf("claims list auto-discover exit = %d, want 1", code)
	}
}

func TestDispatchClaimsRelease(t *testing.T) {
	releaseCalled := false
	var receivedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/claims/release" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		releaseCalled = true
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	os.Setenv("TFSTACKPLAN_SERVER", srv.URL)
	os.Setenv("TFSTACKPLAN_TOKEN", "dummy-token")
	defer func() {
		os.Unsetenv("TFSTACKPLAN_SERVER")
		os.Unsetenv("TFSTACKPLAN_TOKEN")
	}()

	// 1. Release single stack
	code := dispatch([]string{"claims", "release", "--env", "prod", "--pr", "7", "--stack", "stacks/demo"})
	if code != 0 {
		t.Fatalf("claims release exit = %d, want 0", code)
	}
	if !releaseCalled {
		t.Fatal("expected /api/claims/release to be called")
	}
	if receivedBody["environment"] != "prod" || receivedBody["pr"].(float64) != 7 || receivedBody["stack"] != "stacks/demo" {
		t.Errorf("unexpected body payload: %+v", receivedBody)
	}

	// 2. Release all stacks for PR
	releaseCalled = false
	code = dispatch([]string{"claims", "release", "--env", "prod", "--pr", "7"})
	if code != 0 {
		t.Fatalf("claims release exit = %d, want 0", code)
	}
	if !releaseCalled {
		t.Fatal("expected /api/claims/release to be called")
	}
	if receivedBody["stack"] != "" {
		t.Errorf("expected stack to be empty, got %q", receivedBody["stack"])
	}

	// 3. Missing required options
	if code := dispatch([]string{"claims", "release", "--env", "prod"}); code != 2 {
		t.Fatalf("claims release missing --pr exit = %d, want 2", code)
	}

	// 4. Offline / server unset scenario (should return 1)
	os.Unsetenv("TFSTACKPLAN_SERVER")
	if code := dispatch([]string{"claims", "release", "--env", "prod", "--pr", "7"}); code != 1 {
		t.Fatalf("claims release offline exit = %d, want 1", code)
	}
	os.Setenv("TFSTACKPLAN_SERVER", srv.URL)
}

func TestDispatchClaimsBase(t *testing.T) {
	// Missing subcommand
	if code := dispatch([]string{"claims"}); code != 2 {
		t.Fatalf("claims missing subcommand exit = %d, want 2", code)
	}
	// Invalid subcommand
	if code := dispatch([]string{"claims", "invalid"}); code != 2 {
		t.Fatalf("claims invalid subcommand exit = %d, want 2", code)
	}
}

func TestDiscoverEnvironments(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Test config server fallback when stacks directory doesn't exist
	cfgContent := `
server "dev" {
  url = "https://dev-srv"
}
server "prod" {
  url = "https://prod-srv"
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, ".tfstackplan.hcl"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	envs := discoverEnvironments(tmpDir)
	devFound, prodFound := false, false
	for _, env := range envs {
		if env == "dev" {
			devFound = true
		}
		if env == "prod" {
			prodFound = true
		}
	}
	if !devFound || !prodFound {
		t.Errorf("expected dev and prod to be discovered from config, got: %v", envs)
	}

	// 2. Test stacks subdirectory discovery
	if err := os.MkdirAll(filepath.Join(tmpDir, "stacks", "staging"), 0o755); err != nil {
		t.Fatal(err)
	}
	envsSub := discoverEnvironments(tmpDir)
	if len(envsSub) != 1 || envsSub[0] != "staging" {
		t.Errorf("expected staging from stacks subdirectory, got: %v", envsSub)
	}
}
