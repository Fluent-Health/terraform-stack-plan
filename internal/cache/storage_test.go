package cache

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGCSStorage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.Method == "GET" {
			if strings.Contains(r.URL.Path, "missing-key") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if strings.Contains(r.URL.Path, "test-key") {
				if r.URL.Query().Get("alt") == "media" {
					w.Header().Set("Content-Length", "13")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("tarball-bytes"))
				} else {
					// Metadata endpoint for Exists
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"name":"pref/test-key"}`))
				}
				return
			}
		} else if r.Method == "POST" {
			if strings.Contains(r.URL.Path, "/upload/storage/v1/b/") {
				if r.URL.Query().Get("uploadType") == "media" && r.URL.Query().Get("name") == "pref/test-key" {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if string(body) == "tarball-bytes" {
						w.WriteHeader(http.StatusOK)
						return
					}
				}
			}
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	gcs := &GCSStorage{
		bucket:  "bkt",
		prefix:  "pref",
		baseURL: srv.URL,
		token:   func(ctx context.Context) (string, error) { return "token", nil },
		hc:      &http.Client{},
	}

	// 1. Exists checks
	exists, err := gcs.Exists(context.Background(), "test-key")
	if err != nil || !exists {
		t.Errorf("expected key to exist; err=%v, exists=%t", err, exists)
	}
	existsMissing, err := gcs.Exists(context.Background(), "missing-key")
	if err != nil || existsMissing {
		t.Errorf("expected key to be missing; err=%v, exists=%t", err, existsMissing)
	}

	// 2. Get checks
	rc, size, err := gcs.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("expected Get to succeed; err=%v", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(data) != "tarball-bytes" {
		t.Errorf("unexpected content: %s", string(data))
	}
	if size != 13 {
		t.Errorf("unexpected size: %d", size)
	}

	_, _, err = gcs.Get(context.Background(), "missing-key")
	if err == nil {
		t.Error("expected Get of missing-key to fail")
	}

	// 3. Put checks
	err = gcs.Put(context.Background(), "test-key", strings.NewReader("tarball-bytes"), 13)
	if err != nil {
		t.Errorf("expected Put to succeed; err=%v", err)
	}
}
