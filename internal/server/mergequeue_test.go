package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMergeQueueParsesEntries(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t"})
			return
		}
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "mergeQueue") {
			t.Errorf("query missing mergeQueue: %s", body)
		}
		_, _ = io.WriteString(w, `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":{"entries":{"nodes":[
			{"position":1,"state":"MERGEABLE","pullRequest":{"number":774}},
			{"position":2,"state":"QUEUED","pullRequest":{"number":780}}
		]}}}}}`)
	})
	c := newTestRealClient(t)
	res, err := c.MergeQueue(context.Background(), "octo/repo")
	if err != nil {
		t.Fatalf("MergeQueue: %v", err)
	}
	if res.Branch != "main" {
		t.Errorf("branch = %q; want main", res.Branch)
	}
	if len(res.Entries) != 2 || res.Entries[0].PR != 774 || res.Entries[0].Position != 1 || res.Entries[0].State != "MERGEABLE" {
		t.Errorf("entries = %+v", res.Entries)
	}
}

func TestMergeQueueDegradesToEmpty(t *testing.T) {
	// A repo with no merge queue: mergeQueue is null. Must be empty, not error.
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t"})
			return
		}
		_, _ = io.WriteString(w, `{"data":{"repository":{"defaultBranchRef":{"name":"main"},"mergeQueue":null}}}`)
	})
	c := newTestRealClient(t)
	res, err := c.MergeQueue(context.Background(), "octo/repo")
	if err != nil {
		t.Fatalf("MergeQueue: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries = %+v; want empty", res.Entries)
	}
}
