package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testBackend(t *testing.T, handler http.HandlerFunc) *CloudBuild {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cb := NewCloudBuild("proj-1", "asia-south1", map[string]string{"plan": "nonprod-plan", "apply": "nonprod-apply"},
		func(context.Context) (string, error) { return "tok-1", nil })
	cb.base = srv.URL
	return cb
}

func TestCloudBuildStart(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	cb := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"name":"operations/build/proj-1/xyz","metadata":{"@type":"type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata","build":{"id":"build-123","status":"QUEUED"}}}`)
	})

	ref, err := cb.Start(context.Background(), RunRequest{
		Kind: "plan", Environment: "nonprod", SHA: "abc123", Branch: "feat/x",
		ExecutionID: "run-7-nonprod-plan-abc123-a1", PR: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "build-123" {
		t.Errorf("ref = %+v, want build-123", ref)
	}
	if gotPath != "/v1/projects/proj-1/locations/asia-south1/triggers/nonprod-plan:run" {
		t.Errorf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("auth = %q", gotAuth)
	}
	source := gotBody["source"].(map[string]any)
	if source["commitSha"] != "abc123" {
		t.Errorf("commitSha = %v", source["commitSha"])
	}
	if _, hasBranch := source["branchName"]; hasBranch {
		t.Error("branchName must not be set alongside commitSha (oneof)")
	}
	subs := source["substitutions"].(map[string]any)
	if subs["_EXECUTION_ID"] != "run-7-nonprod-plan-abc123-a1" || subs["_PR_NUMBER"] != "7" {
		t.Errorf("substitutions = %v", subs)
	}
}

func TestCloudBuildStartErrors(t *testing.T) {
	cb := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"trigger not found"}}`, http.StatusNotFound)
	})
	if _, err := cb.Start(context.Background(), RunRequest{Kind: "plan", SHA: "x"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
	if _, err := cb.Start(context.Background(), RunRequest{Kind: "deploy", SHA: "x"}); err == nil || !strings.Contains(err.Error(), "no trigger configured") {
		t.Fatalf("want no-trigger error, got %v", err)
	}
}

func TestCloudBuildCancel(t *testing.T) {
	var gotPath string
	status := http.StatusOK
	cb := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(status)
		io.WriteString(w, `{}`)
	})
	if err := cb.Cancel(context.Background(), Ref{ID: "build-123"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/projects/proj-1/locations/asia-south1/builds/build-123:cancel" {
		t.Errorf("path = %s", gotPath)
	}
	// Already-finished (400) and unknown (404) cancels are non-errors.
	for _, s := range []int{http.StatusBadRequest, http.StatusNotFound} {
		status = s
		if err := cb.Cancel(context.Background(), Ref{ID: "build-123"}); err != nil {
			t.Errorf("cancel with %d should be idempotent-ok, got %v", s, err)
		}
	}
	status = http.StatusForbidden
	if err := cb.Cancel(context.Background(), Ref{ID: "build-123"}); err == nil {
		t.Error("cancel with 403 should error")
	}
	if err := cb.Cancel(context.Background(), Ref{}); err != nil {
		t.Errorf("cancel with empty ref is a no-op, got %v", err)
	}
}

func TestCloudBuildProbe(t *testing.T) {
	cases := map[string]Phase{
		"PENDING": PhaseQueued, "QUEUED": PhaseQueued,
		"WORKING": PhaseWorking,
		"SUCCESS": PhaseDone,
		"FAILURE": PhaseFailed, "INTERNAL_ERROR": PhaseFailed, "TIMEOUT": PhaseFailed, "EXPIRED": PhaseFailed, "CANCELLED": PhaseFailed,
	}
	for status, want := range cases {
		cb := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"build-123","status":"`+status+`"}`)
		})
		got, err := cb.Probe(context.Background(), Ref{ID: "build-123"})
		if err != nil || got != want {
			t.Errorf("probe %s = %v, %v; want %v", status, got, err, want)
		}
	}

	cb := testBackend(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	if got, err := cb.Probe(context.Background(), Ref{ID: "gone"}); err != nil || got != PhaseNotFound {
		t.Errorf("probe 404 = %v, %v; want notfound", got, err)
	}
}
