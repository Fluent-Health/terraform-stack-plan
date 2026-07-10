package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestGetExecution(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{
		WebhookSecret: "s3cret",
		APIPrincipals: map[string][]string{"runner@x.iam.gserviceaccount.com": {"report"}},
	})
	a.APIVerifier = fakeOIDC(map[string]string{"tok-runner": "runner@x.iam.gserviceaccount.com"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Seed execution with UpsertInit
	initEv := events.Init{
		ID:          "e1",
		Repo:        "o/r",
		SHA:         "sha123",
		PR:          7,
		Environment: "staging",
		Stacks:      []events.StackState{{Path: "s/a"}},
	}
	if err := store.UpsertInit(db, initEv); err != nil {
		t.Fatalf("UpsertInit: %v", err)
	}

	// Seed gate target
	seedProjectionTarget(t, db, 7, "staging", "gcp-pam", "proj-1", "grant-1", "ACTIVE", "requester-1")

	// Authorization token: OIDC bearer verified by the fakeOIDC verifier above.
	validToken := "tok-runner"

	// 1. Test GET /api/execution/{id} without token (unauthorized)
	{
		req, _ := http.NewRequest("GET", srv.URL+"/api/execution/e1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /api/execution/e1 without auth: got %d, want 401", resp.StatusCode)
		}
	}

	// 2. Test GET /api/execution/non-existent with valid token (not found)
	{
		req, _ := http.NewRequest("GET", srv.URL+"/api/execution/nonexistent", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET /api/execution/nonexistent: got %d, want 404", resp.StatusCode)
		}
	}

	// 3. Test GET /api/execution/{id} with valid token (success)
	{
		req, _ := http.NewRequest("GET", srv.URL+"/api/execution/e1", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/execution/e1 with auth: got %d, want 200", resp.StatusCode)
		}

		var res executionResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode json: %v", err)
		}

		if res.ID != "e1" || res.Repo != "o/r" || res.SHA != "sha123" || res.PR != 7 || res.Environment != "staging" {
			t.Errorf("unexpected execution data in response: %+v", res)
		}

		if len(res.Graph.Stacks) != 1 || res.Graph.Stacks[0].Path != "s/a" {
			t.Errorf("unexpected graph in response: %+v", res.Graph)
		}

		if len(res.Gates) != 1 || res.Gates[0].Class != "gcp-pam" || res.Gates[0].State != "ACTIVE" {
			t.Errorf("unexpected gates in response: %+v", res.Gates)
		}
	}

	// 4. Test GET /api/execution/{id}/events without token (unauthorized)
	{
		req, _ := http.NewRequest("GET", srv.URL+"/api/execution/e1/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /api/execution/e1/events without auth: got %d, want 401", resp.StatusCode)
		}
	}

	// 5. Test GET /api/execution/{id}/events with valid token (SSE stream)
	{
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/execution/e1/events", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/execution/e1/events with auth: got %d, want 200", resp.StatusCode)
		}

		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Fatalf("content-type = %q, want text/event-stream", ct)
		}

		// Channel for scanning lines
		lineCh := make(chan string, 10)
		sc := bufio.NewScanner(resp.Body)
		go func() {
			for sc.Scan() {
				ln := sc.Text()
				if ln != "" {
					lineCh <- ln
				}
			}
		}()

		// Publish standard changed event
		a.hub.publish("exec:e1", "changed")
		select {
		case line := <-lineCh:
			if !strings.Contains(line, "data: changed") {
				t.Errorf("expected changed event data, got: %q", line)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for changed event")
		}

		// Publish superseded event
		a.hub.publish("exec:e1", "superseded:e2")
		// The event message structure:
		// event: superseded
		// data: e2
		select {
		case line := <-lineCh:
			if !strings.Contains(line, "event: superseded") {
				t.Errorf("expected event: superseded, got: %q", line)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for superseded event header")
		}

		select {
		case line := <-lineCh:
			if !strings.Contains(line, "data: e2") {
				t.Errorf("expected data: e2, got: %q", line)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for superseded event data")
		}
	}
}
