package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSeedScenarioHitsEndpoints(t *testing.T) {
	hits := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock responses
		hits[r.URL.Path]++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	planID, applyID, err := SeedScenario(ctx, srv.URL, "test-secret")
	if err != nil {
		t.Fatalf("SeedScenario failed: %v", err)
	}

	if planID == "" || applyID == "" {
		t.Error("expected non-empty plan and apply execution IDs")
	}

	expectedPaths := []string{"/api/init", "/api/update", "/api/finalize", "/api/logs"}
	for _, path := range expectedPaths {
		if hits[path] == 0 {
			t.Errorf("expected endpoint %s to be hit", path)
		}
	}
}

func TestSeedScenarioTransportFailure(t *testing.T) {
	// Start a local HTTP server that always returns an error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated backend crash", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Calling SeedScenario against a failing server must return a non-nil error
	_, _, err := SeedScenario(ctx, srv.URL, "test-secret")
	if err == nil {
		t.Fatal("expected SeedScenario to fail, but it returned no error")
	}

	if !strings.Contains(err.Error(), "status 500") && !strings.Contains(err.Error(), "simulated backend crash") {
		t.Errorf("expected error message to detail the transport failure, got: %v", err)
	}
}
