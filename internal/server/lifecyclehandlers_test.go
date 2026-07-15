package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestGetLifecycleFold(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{Environment: "staging"})

	// A plan execution that progressed warming -> planning -> report,
	// with a two-stack graph carrying op counts.
	plan := events.Init{
		ID: "plan-1", Repo: "o/r", SHA: "sha", PR: 42, Environment: "staging",
		Context: "plan/staging",
		Stacks: []events.StackState{
			{Path: "projects/a", Status: events.StatusPlanned, Counts: &events.Counts{Add: 2}},
			{Path: "projects/b", Status: events.StatusPlanned, Counts: &events.Counts{Change: 1}},
		},
	}
	if err := store.UpsertInit(db, plan); err != nil {
		t.Fatal(err)
	}
	for _, ph := range []events.Phase{events.PhaseWarming, events.PhasePlanning, events.PhaseReport} {
		if err := store.UpsertPhase(db, events.PhaseEvent{ID: "plan-1", Phase: ph, PR: 42, Environment: "staging", Context: "plan/staging"}); err != nil {
			t.Fatal(err)
		}
	}
	// One recorded gate target still AWAITING → classify result "1 gate", an
	// approve segment pending, and apply waiting on approval.
	if _, err := db.Exec(`INSERT INTO gate_targets (pr, environment, class, target, grant_name, state, requester)
		VALUES (42, 'staging', 'iam', 'projects/a', '', 'AWAITING', '')`); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/lifecycle?pr=42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	var out []api.LifecyclePhase
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	byKey := map[string]api.LifecyclePhase{}
	for _, p := range out {
		byKey[p.Key] = p
	}
	// Observed plan-side phases are present and terminal (done) except the last.
	if p, ok := byKey["prepare"]; !ok || p.State != "done" {
		t.Errorf("prepare = %+v; want state done", p)
	}
	if p, ok := byKey["plan"]; !ok || p.State != "done" {
		t.Errorf("plan = %+v; want done", p)
	}
	// report is the last observed phase → now.
	if p, ok := byKey["report"]; !ok || p.State != "now" {
		t.Errorf("report = %+v; want now", p)
	}
	// report result rolls up the plan graph counts.
	if p := byKey["report"]; p.Result == nil || *p.Result != "+2 ~1" {
		t.Errorf("report result = %v; want \"+2 ~1\"", p.Result)
	}
	// classify result reflects the one recorded gate.
	if p, ok := byKey["classify"]; !ok || p.Result == nil || *p.Result != "1 gate required" {
		t.Errorf("classify = %+v; want result \"1 gate required\"", p)
	}
	// approve + apply + verify are unobserved template phases → pending.
	for _, k := range []string{"approve", "apply", "verify"} {
		if p, ok := byKey[k]; !ok || p.State != "pending" {
			t.Errorf("%s = %+v; want pending", k, p)
		}
	}
	if p := byKey["apply"]; p.Result == nil || *p.Result != "waits on approval" {
		t.Errorf("apply result = %v; want \"waits on approval\"", p.Result)
	}
}

func TestGetLifecycleTerminalSuccessIsDone(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{Environment: "staging"})

	// A plan execution that progressed warming -> planning -> report, then
	// reached terminal success.
	plan := events.Init{
		ID: "plan-1", Repo: "o/r", SHA: "sha", PR: 42, Environment: "staging",
		Context: "plan/staging",
		Stacks: []events.StackState{
			{Path: "projects/a", Status: events.StatusPlanned, Counts: &events.Counts{Add: 2}},
		},
	}
	if err := store.UpsertInit(db, plan); err != nil {
		t.Fatal(err)
	}
	for _, ph := range []events.Phase{events.PhaseWarming, events.PhasePlanning, events.PhaseReport} {
		if err := store.UpsertPhase(db, events.PhaseEvent{ID: "plan-1", Phase: ph, PR: 42, Environment: "staging", Context: "plan/staging"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetExecutionStatus(db, "plan-1", "success"); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/lifecycle?pr=42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	var out []api.LifecyclePhase
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	byKey := map[string]api.LifecyclePhase{}
	for _, p := range out {
		byKey[p.Key] = p
	}
	// report is the last observed phase, but its execution reached terminal
	// success → done, not now.
	if p, ok := byKey["report"]; !ok || p.State != "done" {
		t.Errorf("report = %+v; want state done", p)
	}
}

// Three pushes → three live plan executions for the same context. The fold
// must render ONLY the newest run's phases — not concatenate every push's
// prepare/linting/plan/... into a repeating bar.
func TestGetLifecycleUsesNewestExecutionPerContext(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{Environment: "staging"})

	for i, id := range []string{"plan-a1", "plan-a2", "plan-a3"} {
		if err := store.UpsertInit(db, events.Init{
			ID: id, Repo: "o/r", SHA: fmt.Sprintf("sha%d", i), PR: 42, Environment: "staging",
			Context: "plan/staging",
			Stacks:  []events.StackState{{Path: "projects/a", Status: events.StatusPlanned, Counts: &events.Counts{Add: 1}}},
		}); err != nil {
			t.Fatal(err)
		}
		// Deterministic created_at ordering (UpsertInit stamps second-resolution now).
		if _, err := db.Exec(`UPDATE executions SET created_at = ? WHERE id = ?`,
			fmt.Sprintf("2026-07-15 04:%02d:00", 10+i*10), id); err != nil {
			t.Fatal(err)
		}
		for _, ph := range []events.Phase{events.PhaseWarming, events.PhaseLinting, events.PhasePlanning, events.PhaseReport} {
			if err := store.UpsertPhase(db, events.PhaseEvent{ID: id, Phase: ph, PR: 42, Environment: "staging", Context: "plan/staging"}); err != nil {
				t.Fatal(err)
			}
		}
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/lifecycle?pr=42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []api.LifecyclePhase
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	for _, p := range out {
		seen[p.Key]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("key %q appears %d times; every key must appear at most once (got %+v)", k, n, keys(out))
		}
	}
	if seen["prepare"] != 1 || seen["linting"] != 1 || seen["plan"] != 1 || seen["report"] != 1 {
		t.Errorf("expected one prepare/linting/plan/report from the newest run; got %+v", keys(out))
	}
}

// One run emits warming twice (early CI tick + run plan's provider-cache warm,
// with linting between). Both map to the canonical "prepare" — the fold must
// coalesce them into ONE segment, not render prepare,linting,prepare.
func TestGetLifecycleCoalescesRepeatedPhaseKeys(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{Environment: "staging"})

	if err := store.UpsertInit(db, events.Init{
		ID: "plan-1", Repo: "o/r", SHA: "sha", PR: 42, Environment: "staging",
		Context: "plan/staging",
		Stacks:  []events.StackState{{Path: "projects/a", Status: events.StatusPlanned}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, ph := range []events.Phase{events.PhaseWarming, events.PhaseLinting, events.PhaseWarming, events.PhasePlanning} {
		if err := store.UpsertPhase(db, events.PhaseEvent{ID: "plan-1", Phase: ph, PR: 42, Environment: "staging", Context: "plan/staging"}); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/lifecycle?pr=42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []api.LifecyclePhase
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	prepares := 0
	for _, p := range out {
		if p.Key == "prepare" {
			prepares++
		}
	}
	if prepares != 1 {
		t.Errorf("prepare appears %d times; want 1 (got %+v)", prepares, keys(out))
	}
	// The run is mid-planning: plan is the newest observed phase → now.
	byKey := map[string]api.LifecyclePhase{}
	for _, p := range out {
		byKey[p.Key] = p
	}
	if p, ok := byKey["plan"]; !ok || p.State != "now" {
		t.Errorf("plan = %+v; want now", p)
	}
	// The first segment stays prepare (first observation wins the position).
	if len(out) == 0 || out[0].Key != "prepare" {
		t.Errorf("first segment = %+v; want prepare", out)
	}
}

func keys(ps []api.LifecyclePhase) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Key
	}
	return out
}

func TestGetLifecycleUnknownPhasePassesThrough(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{Environment: "staging"})
	if err := store.UpsertInit(db, events.Init{ID: "plan-1", PR: 7, Environment: "staging", Context: "plan/staging"}); err != nil {
		t.Fatal(err)
	}
	// A phase with no canonical mapping must still appear (generic passthrough).
	if err := store.UpsertPhase(db, events.PhaseEvent{ID: "plan-1", Phase: events.PhaseLinting, PR: 7, Environment: "staging", Context: "plan/staging"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/lifecycle?pr=7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []api.LifecyclePhase
	_ = json.NewDecoder(resp.Body).Decode(&out)
	found := false
	for _, p := range out {
		if p.Key == "linting" {
			found = true
		}
	}
	if !found {
		t.Errorf("linting phase not passed through: %+v", out)
	}
}
