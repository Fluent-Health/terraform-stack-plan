package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestGCSBackend(t *testing.T) {
	dir := t.TempDir()
	const cfg = `terraform {
  backend "gcs" {
    bucket = "my-tf-state"
    prefix = "stacks/foo"
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	bucket, prefix, ok := gcsBackend(dir)
	if !ok {
		t.Fatal("gcsBackend: expected ok=true")
	}
	if bucket != "my-tf-state" {
		t.Errorf("bucket = %q, want my-tf-state", bucket)
	}
	if prefix != "stacks/foo" {
		t.Errorf("prefix = %q, want stacks/foo", prefix)
	}

	// A dir with no gcs backend.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "main.tf"), []byte("resource \"null_resource\" \"x\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := gcsBackend(empty); ok {
		t.Error("gcsBackend: expected ok=false for dir without gcs backend")
	}
}

func TestGCSLockerAcquireConflict(t *testing.T) {
	dir := t.TempDir()
	const cfg = `terraform {
  backend "gcs" {
    bucket = "my-tf-state"
    prefix = "stacks/foo"
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var held bool   // lock object currently exists
	var deleted int // count of DELETE calls

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/my-tf-state/o":
			mu.Lock()
			defer mu.Unlock()
			if held {
				// ifGenerationMatch=0 but object exists → precondition failed.
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			held = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"generation":"1"}`))
		case r.Method == http.MethodDelete:
			mu.Lock()
			defer mu.Unlock()
			held = false
			deleted++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	token := func(context.Context) (string, error) { return "test-token", nil }
	lk := newGCSLocker(token, srv.URL)

	// First acquire succeeds.
	release, err := lk.Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Second acquire (lock held) fails fast.
	if _, err := lk.Acquire(context.Background(), dir); err == nil {
		t.Fatal("second Acquire: expected already-locked error, got nil")
	}

	// Release deletes the lock object.
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	mu.Lock()
	if deleted != 1 {
		t.Errorf("DELETE count = %d, want 1", deleted)
	}
	mu.Unlock()

	// After release, acquire succeeds again.
	release2, err := lk.Acquire(context.Background(), dir)
	if err != nil {
		t.Fatalf("third Acquire after release: %v", err)
	}
	_ = release2()
}
