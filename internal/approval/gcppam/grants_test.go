package gcppam

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

func TestBackendImplementsApproval(t *testing.T) {
	var _ approval.Backend = (*Backend)(nil)
}

func TestRequestGrantCreatesThenReuses(t *testing.T) {
	var mu sync.Mutex
	creates := 0
	existing := ""
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/grants"):
			if existing == "" {
				w.Write([]byte(`{"grants":[]}`))
				return
			}
			w.Write([]byte(`{"grants":[{"name":"` + existing + `","state":"APPROVAL_AWAITED","justification":{"unstructuredJustification":"PR #42 env=staging"}}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/grants"):
			if r.Header.Get("Authorization") != "Bearer imp-sa0" {
				t.Errorf("create auth = %q, want imp-sa0", r.Header.Get("Authorization"))
			}
			creates++
			existing = "projects/proj-a/locations/global/entitlements/iam-elev/grants/new"
			w.Write([]byte(`{"name":"` + existing + `"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	req := approval.Request{Class: "iam", Target: "proj-a", PR: 42, Environment: "staging"}

	g1, err := b.RequestGrant(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if g1.State != approval.StateAwaiting || g1.Name == "" {
		t.Errorf("first grant = %+v", g1)
	}
	g2, err := b.RequestGrant(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if creates != 1 {
		t.Errorf("creates = %d, want 1 (reuse the open grant)", creates)
	}
	if g2.Name != g1.Name {
		t.Errorf("reuse mismatch: %q vs %q", g2.Name, g1.Name)
	}
}

func TestRevoke(t *testing.T) {
	var mu sync.Mutex
	revoked := ""
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/grants"):
			w.Write([]byte(`{"grants":[{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/g1","state":"ACTIVE","justification":{"unstructuredJustification":"PR #42 env=staging"}}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, ":revoke"):
			if r.Header.Get("Authorization") != "Bearer adc-token" {
				t.Errorf("revoke should use ADC token, got %q", r.Header.Get("Authorization"))
			}
			revoked = strings.TrimSuffix(r.URL.Path, ":revoke")
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	err := b.Revoke(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 42, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasSuffix(revoked, "/grants/g1") {
		t.Errorf("revoked = %q, want the g1 grant", revoked)
	}
}

func TestRevokeNoMatchingGrant(t *testing.T) {
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"grants":[{"name":"…/grants/other","state":"ACTIVE","justification":{"unstructuredJustification":"PR #99 env=staging"}}]}`))
	})
	if err := b.Revoke(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 42, Environment: "staging"}); err != nil {
		t.Fatalf("revoke with no match should be a no-op, got %v", err)
	}
}
