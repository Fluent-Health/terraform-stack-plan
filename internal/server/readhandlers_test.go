package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
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
