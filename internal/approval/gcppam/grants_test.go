package gcppam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestRequestGrantLeasesUnusedRequester(t *testing.T) {
	var impersonated string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // an open grant held by sa0 (a different PR)
			w.Write([]byte(`{"grants":[{"name":".../grants/g1","state":"ACTIVE","requester":"sa0","justification":{"unstructuredJustification":"PR #1 env=staging"}}]}`))
			return
		}
		w.Write([]byte(`{"name":".../grants/g2"}`)) // create
	}))
	defer srv.Close()
	b := New(
		Config{BaseURL: srv.URL, Entitlements: map[string]string{"iam": "iam-elev"}, RequesterPool: []string{"sa0", "sa1"}},
		func(context.Context) (string, error) { return "t", nil },
		func(_ context.Context, sa string) (string, error) { impersonated = sa; return "imp-" + sa, nil },
	)
	if _, err := b.RequestGrant(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 2, Environment: "staging"}); err != nil {
		t.Fatal(err)
	}
	if impersonated != "sa1" {
		t.Errorf("leased requester = %q, want sa1 (sa0 holds an open grant)", impersonated)
	}
}

func TestListGrantsCapturesRequester(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"grants":[{"name":".../grants/g1","state":"ACTIVE","requester":"sa0","justification":{"unstructuredJustification":"PR #1 env=staging"}}]}`))
	}))
	defer srv.Close()
	b := New(Config{BaseURL: srv.URL, Entitlements: map[string]string{"iam": "iam-elev"}},
		func(context.Context) (string, error) { return "t", nil }, nil)
	gs, err := b.ListGrants(context.Background(), "iam", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Requester != "sa0" {
		t.Fatalf("grants = %+v, want one with Requester sa0", gs)
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

func TestRequestGrantUsesHintedRequester(t *testing.T) {
	var impersonated string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // empty grants list — force create path
			w.Write([]byte(`{"grants":[]}`))
			return
		}
		w.Write([]byte(`{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/hinted"}`))
	}))
	defer srv.Close()
	b := New(
		Config{BaseURL: srv.URL, Entitlements: map[string]string{"iam": "iam-elev"}, RequesterPool: []string{"sa0", "sa1"}},
		func(context.Context) (string, error) { return "t", nil },
		func(_ context.Context, sa string) (string, error) { impersonated = sa; return "imp-" + sa, nil },
	)

	req := approval.Request{Class: "iam", Target: "proj-a", PR: 5, Environment: "prod", Requester: "sa-pinned"}
	g, err := b.RequestGrant(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// (a) must impersonate exactly the hinted SA, not a leased one from the pool
	if impersonated != "sa-pinned" {
		t.Errorf("impersonated = %q, want sa-pinned", impersonated)
	}
	// (b) returned Grant must carry the hinted requester
	if g.Requester != "sa-pinned" {
		t.Errorf("grant.Requester = %q, want sa-pinned", g.Requester)
	}
}

func TestRequestGrantRejectsBadEnvWithoutIO(t *testing.T) {
	// A non-round-trippable environment is rejected before any backend call, so no
	// grant carrying a corrupt justification is ever created.
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("RequestGrant must reject a bad environment before any HTTP call; got %s %s", r.Method, r.URL.Path)
	})
	for _, env := range []string{"stag ing", ""} {
		if _, err := b.RequestGrant(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: env}); err == nil {
			t.Fatalf("RequestGrant must error on environment %q", env)
		}
	}
	// The whitespace case names the cause for the operator.
	_, err := b.RequestGrant(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "stag ing"})
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("whitespace error should mention whitespace, got: %v", err)
	}
}

func TestRequestGrantReturnsSlotCollisionOnForeignGrant(t *testing.T) {
	var createCalls int
	b := fakePAM(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/grants"):
			// A different PR already holds an open grant.
			w.Write([]byte(`{"grants":[{"name":"projects/proj-a/locations/global/entitlements/iam-elev/grants/foreign","state":"ACTIVE","requester":"sa0","justification":{"unstructuredJustification":"PR #99 env=nonprod"}}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/grants"):
			createCalls++
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"message":"INVALID_ARGUMENT: only one open grant per requester"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})

	_, err := b.RequestGrant(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	if err == nil {
		t.Fatal("expected error on slot collision")
	}
	var colErr *approval.SlotCollisionError
	if !errors.As(err, &colErr) {
		t.Fatalf("expected SlotCollisionError, got %T: %v", err, err)
	}
	if colErr.BlockingGrant.Request.PR != 99 {
		t.Errorf("blocking grant PR = %d, want 99", colErr.BlockingGrant.Request.PR)
	}
	if createCalls != 1 {
		t.Errorf("createCalls = %d, want 1 (no retry)", createCalls)
	}
}
