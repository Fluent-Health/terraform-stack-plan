package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBuildDemoAppBootsSuccessfully(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "demo.db")
	app, cleanup, err := buildDemoApp(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to build demo app: %v", err)
	}
	defer cleanup()

	if app.Approval == nil {
		t.Fatal("expected demo app to have approval backend wired")
	}

	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", resp.StatusCode)
	}
}
