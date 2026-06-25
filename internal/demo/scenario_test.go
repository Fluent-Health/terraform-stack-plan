package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
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
