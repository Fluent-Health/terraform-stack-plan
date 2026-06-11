package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
