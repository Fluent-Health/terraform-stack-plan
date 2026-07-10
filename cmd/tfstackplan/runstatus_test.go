package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestRunStatusText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Just a simple route router
	}))
	defer srv.Close()

	// Reconfigure test router to respond appropriately
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/execution/exec-1" {
			http.Error(w, "not found", 404)
			return
		}
		exec := cliExecution{
			Execution: store.Execution{
				ID:          "exec-1",
				Repo:        "owner/repo",
				PR:          101,
				Status:      "success",
				Phase:       "planning",
				Environment: "production",
			},
			Graph: events.Graph{
				Stacks: []events.StackState{
					{
						Path:   "stacks/db",
						Status: events.StatusPlanned,
						Counts: &events.Counts{
							Add:     3,
							Change:  1,
							Destroy: 0,
						},
					},
				},
			},
			Gates: []store.GateTarget{
				{
					Class:  "iam",
					Target: "proj-a",
					State:  "active",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exec)
	})

	out := captureStdout(t, func() int {
		return runStatus([]string{"--server", srv.URL, "--format", "text", "exec-1"})
	})

	expectedSubstrings := []string{
		"Execution ID: exec-1",
		"Repo/PR:      owner/repo #101",
		"Status:       SUCCESS",
		"Phase:        planning",
		"Environment:  production",
		"stacks/db",
		"PLANNED",
		"+3, -0, ~1",
		"iam (proj-a): active",
	}

	for _, sub := range expectedSubstrings {
		if !strings.Contains(out, sub) {
			t.Errorf("expected output to contain %q, but got:\n%s", sub, out)
		}
	}
}

func TestRunStatusJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/execution/exec-1" {
			http.Error(w, "not found", 404)
			return
		}
		exec := cliExecution{
			Execution: store.Execution{
				ID:          "exec-1",
				Repo:        "owner/repo",
				PR:          101,
				Status:      "success",
				Phase:       "planning",
				Environment: "production",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exec)
	}))
	defer srv.Close()

	out := captureStdout(t, func() int {
		return runStatus([]string{"--server", srv.URL, "--format", "json", "exec-1"})
	})

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}
	if parsed["ID"] != "exec-1" {
		t.Errorf("expected parsed ID to be 'exec-1', got %v", parsed["ID"])
	}
}

func TestRunStatusTerminalExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exec := cliExecution{
			Execution: store.Execution{
				ID:     "exec-failed",
				Status: "failure",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exec)
	}))
	defer srv.Close()

	exit := runStatus([]string{"--server", srv.URL, "exec-failed"})
	if exit != 1 {
		t.Fatalf("expected exit 1 for failed status, got %d", exit)
	}
}

func TestRunStatusWatchMode(t *testing.T) {
	var mu sync.Mutex
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.URL.Path == "/api/execution/exec-watch" {
			callCount++
			status := "in_progress"
			if callCount > 1 {
				status = "success"
			}
			exec := cliExecution{
				Execution: store.Execution{
					ID:     "exec-watch",
					Status: status,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(exec)
			return
		}

		if r.URL.Path == "/api/execution/exec-watch/events" {
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", 500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()

			// Write standard SSE event line to trigger a re-fetch
			_, _ = fmt.Fprintf(w, "data: changed\n\n")
			flusher.Flush()
			return
		}

		http.Error(w, "not found", 404)
	}))
	defer srv.Close()

	var exit int
	out := captureStdout(t, func() int {
		exit = runStatus([]string{"--server", srv.URL, "--watch", "exec-watch"})
		return exit
	})

	if exit != 0 {
		t.Fatalf("expected watch mode exit 0, got %d. Output:\n%s", exit, out)
	}

	mu.Lock()
	finalCallCount := callCount
	mu.Unlock()

	if finalCallCount < 2 {
		t.Errorf("expected at least 2 fetches (initial and after event), got %d", finalCallCount)
	}
}
