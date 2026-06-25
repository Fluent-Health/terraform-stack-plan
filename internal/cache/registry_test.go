package cache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistryFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"download_url": "http://example.com/binary.zip"}`))
	}))
	defer srv.Close()

	reg := &RegistryClient{
		baseURL: srv.URL,
		hc:      &http.Client{},
	}

	url, err := reg.GetDownloadURL(context.Background(), Provider{Address: "hashicorp/google", Version: "6.10.0"})
	if err != nil {
		t.Fatalf("unexpected registry fetch error: %v", err)
	}
	if url != "http://example.com/binary.zip" {
		t.Errorf("got url=%q, want http://example.com/binary.zip", url)
	}
}

func TestFetchFromRegistry(t *testing.T) {
	binaryContent := "mock binary content"
	binarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(binaryContent)))
		w.Write([]byte(binaryContent))
	}))
	defer binarySrv.Close()

	registrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"download_url": "%s"}`, binarySrv.URL)))
	}))
	defer registrySrv.Close()

	origClient := defaultRegistry
	defer func() {
		defaultRegistry = origClient
	}()

	defaultRegistry = &RegistryClient{
		baseURL: registrySrv.URL,
		hc:      &http.Client{},
	}

	body, size, err := FetchFromRegistry(context.Background(), Provider{
		Address: "hashicorp/google",
		Version: "6.10.0",
	})
	if err != nil {
		t.Fatalf("unexpected FetchFromRegistry error: %v", err)
	}
	defer body.Close()

	if size != int64(len(binaryContent)) {
		t.Errorf("got size = %d, want %d", size, len(binaryContent))
	}

	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(content) != binaryContent {
		t.Errorf("got content = %q, want %q", string(content), binaryContent)
	}
}
