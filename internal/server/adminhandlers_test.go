package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestAdminGrantsRelease(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	fake := approval.NewFake()
	a.Approval = fake
	fake.RequestGrant(context.Background(), approval.Request{
		Class: "iam", Target: "proj-a", PR: 7, Environment: "staging",
	})

	streamID := execStreamID(7, "staging")
	evs := []reconcile.Event{
		reconcile.Classified{Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}},
		reconcile.GrantObserved{Class: "iam", Target: "proj-a", Name: "grant-1", State: "AWAITING"},
	}
	state := reconcile.ChangeSet{
		PR: 7, Environment: "staging",
		Gate: reconcile.Pending{
			Targets: []reconcile.Target{{Class: "iam", Target: "proj-a", GrantName: "grant-1", Grant: "AWAITING"}},
		},
	}
	if err := a.gateDecider.Append(a.eventStore, streamID, 0, evs, state); err != nil {
		t.Fatal(err)
	}

	_ = a.shell.Handle(context.Background(), 7, "staging", "fluent/repo", reconcile.GateTick{
		Grants: []reconcile.ObservedGrant{{
			Class: "iam", Target: "proj-a", Name: "grant-1", State: "AWAITING",
		}},
	})

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	body, _ := json.Marshal(api.AdminGrantsReleaseRequest{
		Pr:          7,
		Environment: "staging",
		Class:       "iam",
		Target:      "proj-a",
		Reason:      "manual release",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/admin/grants/release", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	if err := a.shell.tick(context.Background(), 7, "staging"); err != nil {
		t.Fatal(err)
	}

	// Verify target state is updated to REVOKED in DB projection
	targets, err := store.TargetsFor(db, 7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].State != "REVOKED" {
		t.Fatalf("target state not updated: %+v", targets)
	}
}

func TestAdminGatesSatisfy(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	fake := approval.NewFake()
	a.Approval = fake
	fake.RequestGrant(context.Background(), approval.Request{
		Class: "iam", Target: "proj-a", PR: 7, Environment: "staging",
	})

	streamID := execStreamID(7, "staging")
	evs := []reconcile.Event{
		reconcile.Classified{Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}},
		reconcile.GrantObserved{Class: "iam", Target: "proj-a", Name: "grant-1", State: "AWAITING"},
	}
	state := reconcile.ChangeSet{
		PR: 7, Environment: "staging",
		Gate: reconcile.Pending{
			Targets: []reconcile.Target{{Class: "iam", Target: "proj-a", GrantName: "grant-1", Grant: "AWAITING"}},
		},
	}
	if err := a.gateDecider.Append(a.eventStore, streamID, 0, evs, state); err != nil {
		t.Fatal(err)
	}

	_ = a.shell.Handle(context.Background(), 7, "staging", "fluent/repo", reconcile.GateTick{
		Grants: []reconcile.ObservedGrant{{
			Class: "iam", Target: "proj-a", Name: "grant-1", State: "AWAITING",
		}},
	})

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	body, _ := json.Marshal(api.AdminGatesSatisfyRequest{
		Pr:          7,
		Environment: "staging",
		Class:       "iam",
		Target:      "proj-a",
		Reason:      "manual satisfy",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/admin/gates/satisfy", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	fake.Approve(approval.Request{
		Class: "iam", Target: "proj-a", PR: 7, Environment: "staging",
	})

	if err := a.shell.tick(context.Background(), 7, "staging"); err != nil {
		t.Fatal(err)
	}

	// Verify target state is updated to ACTIVE in DB projection
	targets, err := store.TargetsFor(db, 7, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].State != "ACTIVE" {
		t.Fatalf("target state not updated: %+v", targets)
	}
}

func TestAdminChecksOverride(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	body, _ := json.Marshal(api.AdminChecksOverrideRequest{
		Pr:          7,
		Environment: "staging",
		Check:       "terraform/staging",
		Conclusion:  "success",
		Reason:      "bypass",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/admin/checks/override", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	// Load decider state to verify check override is loaded
	cs, _, err := a.gateDecider.Load(a.eventStore, execStreamID(7, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	if cs.CheckOverride == nil || cs.CheckOverride.Conclusion != "success" {
		t.Fatalf("check override not set in ChangeSet: %+v", cs.CheckOverride)
	}
}

func TestAdminExecutionsCancel(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})

	// Seed an execution
	seedInit(t, a.shell, events.Init{
		ID:          "exec-123",
		PR:          7,
		Environment: "staging",
		Repo:        "fluent/repo",
		SHA:         "abcdef",
	})

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	body, _ := json.Marshal(api.AdminExecutionsCancelRequest{
		Id:     "exec-123",
		Reason: "hung build",
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/admin/executions/cancel", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}

	// Verify the execution run is completed (no longer live) in decider
	cs, _, err := a.gateDecider.Load(a.eventStore, execStreamID(7, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	run, exists := cs.Runs[""] // Phase was "" so Key is ""
	if exists && run.Live() {
		t.Fatalf("expected run to be non-live, got phase %s", run.Phase)
	}
}
