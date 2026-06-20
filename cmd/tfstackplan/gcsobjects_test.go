package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestGCSObjectStorePutGet(t *testing.T) {
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/upload/"):
			b, _ := io.ReadAll(r.Body)
			objects[r.URL.Query().Get("name")] = b
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
		case r.Method == "GET":
			i := strings.LastIndex(r.URL.Path, "/o/")
			name, _ := url.QueryUnescape(r.URL.Path[i+3:])
			if b, ok := objects[name]; ok {
				w.WriteHeader(200)
				w.Write(b)
			} else {
				http.Error(w, "not found", http.StatusNotFound)
			}
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	tok := func(context.Context) (string, error) { return "t", nil }
	s := newGCSObjectStore(tok, "bkt", "logs", srv.URL)

	if err := s.Put(context.Background(), "executions/e1/s/log", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if _, ok := objects["logs/executions/e1/s/log"]; !ok {
		t.Fatalf("object not stored under prefix; have %v", objects)
	}
	rc, err := s.Get(context.Background(), "executions/e1/s/log")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Errorf("get = %q, want hello", got)
	}
	if _, err := s.Get(context.Background(), "nope"); err == nil {
		t.Error("Get of a missing object should error")
	}
}

func TestGCSPutStreamsFile(t *testing.T) {
	var got []byte
	var gotLen int64 = -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A real file is the offload path's reader.
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("terraform apply line\n"), 1000)
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, 0)

	s := newGCSObjectStore(func(context.Context) (string, error) { return "tok", nil }, "b", "p", srv.URL)
	if err := s.Put(context.Background(), "k", f); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("uploaded %d bytes, want %d (round-trip mismatch)", len(got), len(content))
	}
	if gotLen != int64(len(content)) {
		t.Errorf("Content-Length = %d, want %d (streamed file should set it)", gotLen, len(content))
	}
}

func TestGCSPutFallbackNonFile(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := newGCSObjectStore(func(context.Context) (string, error) { return "tok", nil }, "b", "p", srv.URL)
	if err := s.Put(context.Background(), "k", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("fallback upload = %q, want hello", got)
	}
}
