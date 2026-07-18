package execution

import (
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestEvolveStartedReplacesState(t *testing.T) {
	prior := State{ID: "old", Phase: events.PhaseApplying}
	got := Evolve(prior, Started{Exec: State{ID: "e1", Stacks: []Stack{{Path: "a"}}}})
	if got.ID != "e1" || len(got.Stacks) != 1 || got.Phase != "" {
		t.Fatalf("Started must replace whole state, got %#v", got)
	}
}

func TestEvolvePhaseChanged(t *testing.T) {
	got := Evolve(State{ID: "e1"}, PhaseChanged{Phase: events.PhaseApplying})
	if got.Phase != events.PhaseApplying || got.ID != "e1" {
		t.Fatalf("want phase applying, id kept; got %#v", got)
	}
}

func TestEvolveStackStatusChanged(t *testing.T) {
	prior := State{Stacks: []Stack{{Path: "a"}, {Path: "b"}}}
	got := Evolve(prior, StackStatusChanged{Stack: "b", Status: events.StatusFailed, Detail: "boom"})
	if got.Stacks[0].RunStatus != "" {
		t.Fatalf("stack a must be untouched, got %q", got.Stacks[0].RunStatus)
	}
	if got.Stacks[1].RunStatus != events.StatusFailed || got.Stacks[1].Detail != "boom" {
		t.Fatalf("stack b not updated: %#v", got.Stacks[1])
	}
}

func TestEvolveFailedAbortsInnocentStacks(t *testing.T) {
	prior := State{Stacks: []Stack{
		{Path: "pend", RunStatus: events.StatusPending},
		{Path: "run", RunStatus: events.StatusRunning},
		{Path: "init", RunStatus: events.StatusInitializing},
		{Path: "inited", RunStatus: events.StatusInitialized},
		{Path: "moving", RunStatus: events.StatusMoving},
		{Path: "done", RunStatus: events.StatusPlanned},
		{Path: "bad", RunStatus: events.StatusFailed},
	}}
	got := Evolve(prior, Failed{})
	want := map[string]events.Status{
		"pend": events.StatusAborted, "run": events.StatusAborted,
		"init": events.StatusAborted, "inited": events.StatusAborted,
		"moving": events.StatusAborted, // moving is innocent → aborted (live behavior)
		"done":   events.StatusPlanned, // terminal statuses are untouched
		"bad":    events.StatusFailed,
	}
	for _, s := range got.Stacks {
		if s.RunStatus != want[s.Path] {
			t.Fatalf("stack %q: want %q, got %q", s.Path, want[s.Path], s.RunStatus)
		}
	}
}

func TestEvolveFoldSequence(t *testing.T) {
	var s State
	for _, e := range []Event{
		Started{Exec: State{ID: "e1", Stacks: []Stack{{Path: "a"}}}},
		PhaseChanged{Phase: events.PhaseApplying},
		StackStatusChanged{Stack: "a", Status: events.StatusRunning},
		Failed{},
	} {
		s = Evolve(s, e)
	}
	want := State{
		ID:     "e1",
		Phase:  events.PhaseApplying,
		Status: "failure",
		Stacks: []Stack{{Path: "a", RunStatus: events.StatusAborted}},
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("fold mismatch:\n got  %#v\n want %#v", s, want)
	}
}

func TestEvolveStartedCarriesIdentityAndEdges(t *testing.T) {
	got := Evolve(State{}, Started{Exec: State{
		ID: "e1", PR: 7, Environment: "nonprod", Context: "terraform/nonprod",
		Status: "in_progress",
		Stacks: []Stack{{Path: "a"}}, Edges: []Edge{{From: "a", To: "b"}},
	}})
	if got.PR != 7 || got.Environment != "nonprod" || got.Context != "terraform/nonprod" {
		t.Fatalf("identity not folded: %#v", got)
	}
	if got.Status != "in_progress" || len(got.Edges) != 1 {
		t.Fatalf("status/edges not folded: %#v", got)
	}
}

func TestEvolvePhaseChangedFoldsProgress(t *testing.T) {
	pct := 42
	got := Evolve(State{ID: "e1"}, PhaseChanged{Phase: events.PhaseApplying, Label: "applying 3/8", Pct: &pct})
	if got.Phase != events.PhaseApplying || got.ProgressLabel != "applying 3/8" || got.ProgressPct == nil || *got.ProgressPct != 42 {
		t.Fatalf("progress not folded: %#v", got)
	}
}

func TestEvolvePhaseChangedSetsIdentityNonRegressively(t *testing.T) {
	// Phase-before-init: identity fields materialize the row.
	got := Evolve(State{}, PhaseChanged{Phase: events.PhaseInitializing, ID: idOrEmpty(), PR: 9, Environment: "prod", Context: "terraform/prod", Repo: "r", SHA: "abc"})
	if got.PR != 9 || got.Environment != "prod" || got.Repo != "r" {
		t.Fatalf("identity not set on empty state: %#v", got)
	}
	// A later PhaseChanged with zero identity must NOT clobber it.
	got2 := Evolve(got, PhaseChanged{Phase: events.PhaseApplying})
	if got2.PR != 9 || got2.Environment != "prod" || got2.Repo != "r" {
		t.Fatalf("identity regressed: %#v", got2)
	}
}

func TestEvolveFailedSetsRunStatus(t *testing.T) {
	got := Evolve(State{Status: "in_progress", Stacks: []Stack{{Path: "a", RunStatus: events.StatusRunning}}}, Failed{})
	if got.Status != "failure" {
		t.Fatalf("run status = %q, want failure", got.Status)
	}
	if got.Stacks[0].RunStatus != events.StatusAborted {
		t.Fatalf("stack not aborted: %q", got.Stacks[0].RunStatus)
	}
}

func TestEvolveSucceededSetsRunStatus(t *testing.T) {
	got := Evolve(State{Status: "in_progress"}, Succeeded{})
	if got.Status != "success" {
		t.Fatalf("run status = %q, want success", got.Status)
	}
}

func TestEvolveStacksAnnotated(t *testing.T) {
	prior := State{Stacks: []Stack{
		{Path: "a", RunStatus: events.StatusPlanned},
		{Path: "b", RunStatus: events.StatusPlanned},
		{Path: "c", RunStatus: events.StatusFailed},
	}}
	cnt := events.Counts{}
	got := Evolve(prior, StacksAnnotated{
		Projects:   map[string]string{"a": "proj-a"},
		Categories: map[string][]events.Category{"a": {events.Category{Name: "iam"}}},
		Counts:     map[string]events.Counts{"a": cnt},
		Moving:     []string{"b", "c"}, // c is failed → must NOT become moving
	})
	if got.Stacks[0].Project != "proj-a" || len(got.Stacks[0].Categories) != 1 || got.Stacks[0].Counts == nil {
		t.Fatalf("annotate not folded onto a: %#v", got.Stacks[0])
	}
	if got.Stacks[1].RunStatus != events.StatusMoving {
		t.Fatalf("b should be moving: %q", got.Stacks[1].RunStatus)
	}
	if got.Stacks[2].RunStatus != events.StatusFailed {
		t.Fatalf("c (failed) must stay failed: %q", got.Stacks[2].RunStatus)
	}
}

func TestEvolveSupersededSetsSupersededBy(t *testing.T) {
	got := Evolve(State{ID: "old"}, Superseded{By: "new-exec"})
	if got.SupersededBy != "new-exec" {
		t.Fatalf("SupersededBy = %q, want new-exec", got.SupersededBy)
	}
}

func TestEvolveStartedClearsSupersededBy(t *testing.T) {
	// A fresh Init un-supersedes a revived execID (the superseded_by half of the
	// old ReviveExecution): Started folds ev.Exec, whose SupersededBy is empty.
	prior := Evolve(State{ID: "e1"}, Superseded{By: "n1"})
	if prior.SupersededBy != "n1" {
		t.Fatalf("precondition: want n1, got %q", prior.SupersededBy)
	}
	got := Evolve(prior, Started{Exec: State{ID: "e1", Stacks: []Stack{{Path: "a"}}}})
	if got.SupersededBy != "" {
		t.Fatalf("Started must clear SupersededBy, got %q", got.SupersededBy)
	}
}

func idOrEmpty() string { return "e9" }

// TestEvolveStartedIsNonRegressive captures the register→plan invariant: a repeat
// Init (same execution id, e.g. `run register` followed by `run plan`) must not
// regress an already-advanced stack's runner-told progress back to pending —
// mirrors the old store.UpsertInit, whose stack upsert only ever refreshed
// `project` on conflict.
func TestEvolveStartedIsNonRegressive(t *testing.T) {
	var s State
	s = Evolve(s, Started{Exec: State{ID: "e1", Stacks: []Stack{
		{Path: "a", RunStatus: events.StatusPending},
		{Path: "b", RunStatus: events.StatusPending},
		{Path: "c", RunStatus: events.StatusPending},
	}}})
	s = Evolve(s, StackStatusChanged{Stack: "a", Status: events.StatusInitialized})

	// Second Init omits "c" (absent from the reported subgraph) and refreshes
	// project on "a"; all three stacks are reported pending again.
	got := Evolve(s, Started{Exec: State{ID: "e1", Stacks: []Stack{
		{Path: "a", Project: "proj-a", RunStatus: events.StatusPending},
		{Path: "b", RunStatus: events.StatusPending},
	}}})

	byPath := make(map[string]Stack, len(got.Stacks))
	for _, st := range got.Stacks {
		byPath[st.Path] = st
	}

	a, ok := byPath["a"]
	if !ok {
		t.Fatalf("stack a missing: %#v", got.Stacks)
	}
	if a.RunStatus != events.StatusInitialized {
		t.Fatalf("stack a regressed: want %q, got %q", events.StatusInitialized, a.RunStatus)
	}
	if a.Project != "proj-a" {
		t.Fatalf("stack a project not refreshed from 2nd Init: got %q", a.Project)
	}

	b, ok := byPath["b"]
	if !ok {
		t.Fatalf("stack b missing: %#v", got.Stacks)
	}
	if b.RunStatus != events.StatusPending {
		t.Fatalf("stack b should stay pending, got %q", b.RunStatus)
	}

	c, ok := byPath["c"]
	if !ok {
		t.Fatalf("prior-only stack c dropped, want carried forward: %#v", got.Stacks)
	}
	if c.RunStatus != events.StatusPending {
		t.Fatalf("carried-forward stack c status changed unexpectedly: got %q", c.RunStatus)
	}

	// Deterministic ordering: Init stacks first (a, b), then carried-forward
	// prior-only stacks (c).
	wantOrder := []string{"a", "b", "c"}
	for i, path := range wantOrder {
		if got.Stacks[i].Path != path {
			t.Fatalf("stack order[%d] = %q, want %q (full: %#v)", i, got.Stacks[i].Path, path, got.Stacks)
		}
	}
}

// TestEvolveStartedDoesNotMutateInputEvent guards against Evolve writing through
// the Started event's own Exec.Stacks backing array. shell.go folds evs with
// Evolve and then re-persists the SAME evs slice via Append — any in-place
// mutation here would silently corrupt the event about to be appended to the
// log, violating Evolve's purity and the event log's source-of-truth invariant.
func TestEvolveStartedDoesNotMutateInputEvent(t *testing.T) {
	prior := Evolve(Evolve(State{}, Started{Exec: State{Stacks: []Stack{
		{Path: "a", RunStatus: events.StatusPending},
	}}}), StackStatusChanged{Stack: "a", Status: events.StatusInitialized})

	ev2 := Started{Exec: State{Stacks: []Stack{
		{Path: "a", RunStatus: events.StatusPending},
	}}}

	_ = Evolve(prior, ev2)

	if ev2.Exec.Stacks[0].RunStatus != events.StatusPending {
		t.Fatalf("Evolve mutated the input event's Exec.Stacks: got RunStatus %q, want %q (unmutated)", ev2.Exec.Stacks[0].RunStatus, events.StatusPending)
	}
}
