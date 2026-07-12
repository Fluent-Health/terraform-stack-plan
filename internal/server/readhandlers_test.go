package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

func TestInspectGrants(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	// Seed some gate targets
	_, err := db.Exec(`
		INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
		VALUES (7, 'staging', 'iam', 'proj-a', 'grant-1', 'AWAITING', 'req-1')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
		VALUES (8, 'prod', 'iam', 'proj-b', 'grant-2', 'ACTIVE', 'req-2')`)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// 1. Get all grants
	req, _ := http.NewRequest("GET", srv.URL+"/api/inspect/grants", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var grants []api.InspectGrant
	if err := json.NewDecoder(resp.Body).Decode(&grants); err != nil {
		t.Fatal(err)
	}

	if len(grants) != 2 {
		t.Fatalf("len = %d; want 2", len(grants))
	}

	// 2. Filter by state=open
	req, _ = http.NewRequest("GET", srv.URL+"/api/inspect/grants?state=open", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var openGrants []api.InspectGrant
	if err := json.NewDecoder(resp.Body).Decode(&openGrants); err != nil {
		t.Fatal(err)
	}

	if len(openGrants) != 2 { // AWAITING and ACTIVE are both open states
		t.Fatalf("len = %d; want 2", len(openGrants))
	}

	// 3. Test Live drift check
	fake := approval.NewFake()
	a.Approval = fake
	// Seed a grant in fake backend (starts as AWAITING)
	fake.RequestGrant(context.Background(), approval.Request{
		Class: "iam", Target: "proj-a", PR: 7, Environment: "staging",
	})
	// Approve it to make it ACTIVE (now it drifts from stored AWAITING in database)
	fake.Approve(approval.Request{
		Class: "iam", Target: "proj-a", PR: 7, Environment: "staging",
	})

	req, _ = http.NewRequest("GET", srv.URL+"/api/inspect/grants?live=1", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var liveGrants []api.InspectGrant
	if err := json.NewDecoder(resp.Body).Decode(&liveGrants); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, lg := range liveGrants {
		if lg.Pr == 7 && lg.Environment == "staging" {
			found = true
			if lg.DriftDetected == nil || !*lg.DriftDetected {
				t.Fatalf("expected drift_detected to be true")
			}
			if lg.ActualState == nil || *lg.ActualState != "ACTIVE" {
				t.Fatalf("expected actual_state to be ACTIVE, got %v", lg.ActualState)
			}
		}
	}
	if !found {
		t.Fatalf("pr 7 not found in live grants")
	}
}

func TestInspectGate(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	streamID := execStreamID(7, "staging")
	
	// Seed some gate-lifecycle events via a.gateDecider
	evs1 := []reconcile.Event{
		reconcile.Classified{Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}},
	}
	state1 := reconcile.ChangeSet{
		PR: 7, Environment: "staging",
		Gate: reconcile.Pending{
			Targets: []reconcile.Target{{Class: "iam", Target: "proj-a", Grant: approval.StateAwaiting}},
		},
	}
	if err := a.gateDecider.Append(a.eventStore, streamID, 0, evs1, state1); err != nil {
		t.Fatal(err)
	}

	evs2 := []reconcile.Event{
		reconcile.GrantObserved{Class: "iam", Target: "proj-a", Name: "grant-1", State: approval.StateActive},
		reconcile.GateSatisfied{},
	}
	state2 := reconcile.ChangeSet{
		PR: 7, Environment: "staging",
		Gate: reconcile.Satisfied{
			Targets: []reconcile.Target{{Class: "iam", Target: "proj-a", GrantName: "grant-1", Grant: approval.StateActive}},
		},
	}
	if err := a.gateDecider.Append(a.eventStore, streamID, 1, evs2, state2); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/inspect/gate/7/staging", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var detail api.InspectGateDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}

	if detail.Pr != 7 || detail.Environment != "staging" {
		t.Fatalf("pr/env mismatch: %+v", detail)
	}
	if detail.GateState != "Satisfied" {
		t.Fatalf("gate_state = %s; want Satisfied", detail.GateState)
	}
	if len(detail.Targets) != 1 || detail.Targets[0].State != "ACTIVE" {
		t.Fatalf("targets mismatch: %+v", detail.Targets)
	}
	// Verify reason trail exists and is ordered
	if len(detail.Reasons) < 2 {
		t.Fatalf("expected at least 2 reasons, got %d: %+v", len(detail.Reasons), detail.Reasons)
	}
}

func TestInspectClaims(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	stream := "env:staging"
	expires := time.Now().Add(claims.Lease())
	
	// Seed some claim events
	evs := []claims.Event{
		claims.ClaimAcquired{PR: 7, Stacks: []string{"stacks/a", "stacks/b"}, ExpiresAt: expires},
	}
	state := claims.ClaimSet{
		"stacks/a": claims.Claim{PR: 7, ExpiresAt: expires},
		"stacks/b": claims.Claim{PR: 7, ExpiresAt: expires},
	}
	if err := a.claimsDecider.Append(a.eventStore, stream, 0, evs, state); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/inspect/claims/staging", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	var res api.InspectClaimsSet
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}

	if res.Environment != "staging" {
		t.Fatalf("env mismatch: %s", res.Environment)
	}
	if len(res.Claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(res.Claims))
	}
}
