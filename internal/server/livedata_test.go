package server

import (
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestGroupStacksByKey(t *testing.T) {
	stacks := []events.StackState{
		{Path: "nonprod/pipelines/x"},
		{Path: "nonprod/projects/a"},
		{Path: "nonprod/pipelines/y"},
	}
	groups := groupStacksByKey(stacks, 2, nil)
	if len(groups) != 2 ||
		groups[0].Name != "nonprod/pipelines" || len(groups[0].Stacks) != 2 ||
		groups[1].Name != "nonprod/projects" {
		t.Fatalf("groups = %+v, want nonprod/pipelines(2), nonprod/projects(1)", groups)
	}
}

func TestStatusBadge(t *testing.T) {
	cases := map[events.Status]string{
		events.StatusPending: "badge-ghost",
		events.StatusPlanned: "badge-info",
		events.StatusGated:   "badge-warning",
		events.StatusSafe:    "badge-success",
		events.StatusMoving:  "badge-info",
		events.StatusFailed:  "badge-error",
	}
	for s, want := range cases {
		if got := statusBadge(s); got != want {
			t.Errorf("statusBadge(%q) = %q, want %q", s, got, want)
		}
	}
	if got := statusBadge(events.Status("weird")); got != "badge-ghost" {
		t.Errorf("unknown status badge = %q, want badge-ghost", got)
	}
}

func TestAggregateVerdict(t *testing.T) {
	stacks := []events.StackState{
		{Path: "a", Counts: &events.Counts{Add: 6}},
		{Path: "b", Counts: &events.Counts{Change: 8, Replace: 3}},
		{Path: "c", Counts: &events.Counts{Destroy: 2, Move: 1}},
		{Path: "d"}, // no counts → contributes nothing
	}
	v := aggregateVerdict(stacks)
	if v.Add != 6 || v.Change != 8 || v.Replace != 3 || v.Destroy != 2 || v.Move != 1 {
		t.Fatalf("bad totals: %+v", v)
	}
	if v.TotalOps != 19 { // Add+Change+Destroy+Replace = 6+8+2+3 = 19
		t.Fatalf("TotalOps=%d", v.TotalOps)
	}
}

func TestDisplayStatePhase(t *testing.T) {
	cases := []struct {
		st    events.Status
		kind  string
		phase events.Phase
		label string
		css   string
	}{
		// plan kind: phase ignored
		{events.StatusPending, "plan", events.PhasePlanning, "queued", "queued"},
		{events.StatusRunning, "plan", events.PhasePlanning, "planning", "planning"},
		{events.StatusPlanned, "plan", events.PhasePlanning, "planned", "planned"},
		{events.StatusSafe, "plan", events.PhasePlanning, "planned", "planned"},
		// new init states
		{events.StatusInitializing, "plan", events.PhaseInitializing, "initializing", "initializing"},
		{events.StatusInitialized, "plan", events.PhaseInitializing, "initialized", "initialized"},
		// apply, pre-apply re-plan pass (phase < applying): preparing/prepared
		{events.StatusRunning, "apply", events.PhaseInitializing, "preparing", "preparing"},
		{events.StatusPlanned, "apply", events.PhaseInitializing, "prepared", "prepared"},
		{events.StatusRunning, "apply", events.PhasePlanning, "preparing", "preparing"},
		// apply, real apply (phase >= applying): applying/applied
		{events.StatusRunning, "apply", events.PhaseApplying, "applying", "applying"},
		{events.StatusMoving, "apply", events.PhaseApplying, "moving", "moving"},
		{events.StatusGated, "apply", events.PhaseApplying, "blocked", "blocked"},
		{events.StatusAborted, "apply", events.PhaseApplying, "aborted", "aborted"},
		{events.StatusPlanned, "apply", events.PhaseApplying, "queued", "queued"},
		{events.StatusSafe, "apply", events.PhaseApplying, "applied", "applied"},
		{events.StatusSafe, "apply", events.PhaseInitializing, "applied", "applied"},
		{events.StatusNochange, "apply", events.PhaseApplying, "no changes", "nochange"},
	}
	for _, c := range cases {
		got := displayState(c.st, c.kind, c.phase)
		if got.Label != c.label || got.CSS != c.css {
			t.Errorf("displayState(%q,%q,%q) = {%q,%q}, want {%q,%q}",
				c.st, c.kind, c.phase, got.Label, got.CSS, c.label, c.css)
		}
	}
}

func TestOpSummaryString(t *testing.T) {
	if got := opSummary(&events.Counts{Add: 6}); got != "+6" {
		t.Fatalf("got %q", got)
	}
	if got := opSummary(&events.Counts{Change: 8}); got != "~8" {
		t.Fatalf("got %q", got)
	}
	if got := opSummary(&events.Counts{Replace: 3}); got != "±3" {
		t.Fatalf("got %q", got)
	}
	if got := opSummary(&events.Counts{Destroy: 2}); got != "−2" {
		t.Fatalf("destroy: got %q", got)
	}
	if got := opSummary(&events.Counts{Move: 1}); got != "↔1" {
		t.Fatalf("move: got %q", got)
	}
	if got := opSummary(nil); got != "" {
		t.Fatalf("nil → %q want empty", got)
	}
}

func TestGroupByProject(t *testing.T) {
	stacks := []events.StackState{
		{Path: "prod/a", Project: "fh-prod-host"},
		{Path: "prod/b", Project: "fh-prod-host"},
		{Path: "stg/c", Project: "fh-staging-host"},
		{Path: "x/d"}, // no project → trailing ungrouped bucket
	}
	gs := groupByProject(stacks)
	if len(gs) != 3 {
		t.Fatalf("want 3 groups, got %d", len(gs))
	}
	if gs[0].Name != "fh-prod-host" || len(gs[0].Stacks) != 2 {
		t.Fatalf("group0 = %+v", gs[0])
	}
	if gs[1].Name != "fh-staging-host" {
		t.Fatalf("group1 = %+v", gs[1])
	}
	if gs[2].Name != "—" || len(gs[2].Stacks) != 1 { // ungrouped bucket LAST
		t.Fatalf("ungrouped bucket wrong: %+v", gs[2])
	}
}

func TestProgressSegments(t *testing.T) {
	stacks := []events.StackState{
		{Path: "a", Counts: &events.Counts{Add: 6}, Status: events.StatusSafe},
		{Path: "b", Counts: &events.Counts{Destroy: 2}, Status: events.StatusRunning},
		{Path: "c", Status: events.StatusFailed}, // no counts → min flex 1
	}
	segs := progressSegments(stacks, "apply", events.PhaseApplying)
	if len(segs) != 3 {
		t.Fatalf("want 3 segs, got %d", len(segs))
	}
	// Rank-sorted: applied(0) < failed(1) < applying(2).
	// Flex sized by ops; colour = the stack's CURRENT state (apply kind).
	if segs[0].Flex != 6 || segs[0].StateCSS != "applied" {
		t.Fatalf("seg0=%+v", segs[0])
	}
	if segs[1].Flex != 1 || segs[1].StateCSS != "failed" {
		t.Fatalf("seg1=%+v", segs[1])
	}
	if segs[2].Flex != 2 || segs[2].StateCSS != "applying" {
		t.Fatalf("seg2=%+v", segs[2])
	}
}

func TestIAMCount(t *testing.T) {
	stacks := []events.StackState{
		{Categories: []events.Category{{Name: "iam"}}},
		{Categories: []events.Category{{Name: "destructive"}}},
		{Categories: []events.Category{{Name: "iam"}, {Name: "destructive"}}},
		{},
	}
	if n := iamCount(stacks); n != 2 {
		t.Fatalf("iamCount=%d want 2", n)
	}
}

func TestRiskTags(t *testing.T) {
	s := events.StackState{Categories: []events.Category{{Name: "iam", Icon: "🔐"}, {Name: "destructive", Icon: "💣"}}}
	tags := riskTags(s)
	if len(tags) != 2 || tags[0].CSS != "iam" || tags[1].CSS != "danger" {
		t.Fatalf("tags=%+v", tags)
	}
	if len(riskTags(events.StackState{})) != 0 {
		t.Fatal("no categories → no tags")
	}
}

func TestApplyBadgePreparing(t *testing.T) {
	mk := func(phase events.Phase, finished bool, status string) string {
		v := liveView{Phase: phase, Status: status}
		return buildLiveModel(v, "apply", finished, time.Now()).PhaseLabel
	}
	if got := mk(events.PhaseInitializing, false, ""); got != "PREPARING" {
		t.Errorf("apply initializing badge = %q, want PREPARING", got)
	}
	if got := mk(events.PhasePlanning, false, ""); got != "PREPARING" {
		t.Errorf("apply planning badge = %q, want PREPARING", got)
	}
	if got := mk(events.PhaseApplying, false, ""); got != "APPLYING" {
		t.Errorf("apply applying badge = %q, want APPLYING", got)
	}
	if got := mk(events.PhaseVerifying, false, ""); got != "VERIFYING" {
		t.Errorf("apply verifying badge = %q, want VERIFYING", got)
	}
	if got := mk(events.PhaseApplying, true, "failure"); got != "FAILED" {
		t.Errorf("apply finished failure badge = %q, want FAILED", got)
	}
}

func TestPhaseTimeline(t *testing.T) {
	t.Run("plan in-progress", func(t *testing.T) {
		steps := phaseTimeline("plan", events.PhasePlanning, false)
		if len(steps) != 2 {
			t.Fatalf("plan timeline: got %d steps, want 2", len(steps))
		}
		if steps[0].Name != "Plan" || steps[0].State != "active" {
			t.Errorf("plan step 0: got {%q, %q}, want {Plan, active}", steps[0].Name, steps[0].State)
		}
		if steps[1].Name != "Report" || steps[1].State != "todo" {
			t.Errorf("plan step 1: got {%q, %q}, want {Report, todo}", steps[1].Name, steps[1].State)
		}
	})

	t.Run("plan finished", func(t *testing.T) {
		steps := phaseTimeline("plan", events.PhasePlanning, true)
		for _, st := range steps {
			if st.State != "done" {
				t.Errorf("finished plan: step %q = %q, want done", st.Name, st.State)
			}
		}
	})

	t.Run("apply in-progress", func(t *testing.T) {
		steps := phaseTimeline("apply", events.PhaseApplying, false)
		if len(steps) != 4 {
			t.Fatalf("apply timeline: got %d steps, want 4", len(steps))
		}
		// Warming and Initializing precede Apply — both must be done.
		if steps[0].Name != "Warming" || steps[0].State != "done" {
			t.Errorf("apply step 0: got {%q, %q}, want {Warming, done}", steps[0].Name, steps[0].State)
		}
		if steps[1].Name != "Initializing" || steps[1].State != "done" {
			t.Errorf("apply step 1: got {%q, %q}, want {Initializing, done}", steps[1].Name, steps[1].State)
		}
		if steps[2].Name != "Apply" || steps[2].State != "active" {
			t.Errorf("apply step 2: got {%q, %q}, want {Apply, active}", steps[2].Name, steps[2].State)
		}
		if steps[3].Name != "Verify" || steps[3].State != "todo" {
			t.Errorf("apply step 3: got {%q, %q}, want {Verify, todo}", steps[3].Name, steps[3].State)
		}
	})

	t.Run("apply finished", func(t *testing.T) {
		steps := phaseTimeline("apply", events.PhaseVerifying, true)
		for _, st := range steps {
			if st.State != "done" {
				t.Errorf("finished apply: step %q = %q, want done", st.Name, st.State)
			}
		}
	})

	t.Run("unknown phase is all todo", func(t *testing.T) {
		for _, st := range phaseTimeline("plan", events.Phase(""), false) {
			if st.State != "todo" {
				t.Errorf("empty-phase step %s = %q, want todo", st.Name, st.State)
			}
		}
	})
}

func TestProgressSegmentsOrder(t *testing.T) {
	stacks := []events.StackState{
		{Path: "a", Status: events.StatusPending},      // queued  → rank 5
		{Path: "b", Status: events.StatusSafe},         // planned → rank 0
		{Path: "c", Status: events.StatusRunning},      // planning→ rank 2
		{Path: "d", Status: events.StatusFailed},       // failed  → rank 1
		{Path: "e", Status: events.StatusInitializing}, // rank 4
	}
	segs := progressSegments(stacks, "plan", events.PhasePlanning)
	got := make([]string, len(segs))
	for i, s := range segs {
		got[i] = s.StateCSS
	}
	want := []string{"planned", "failed", "planning", "initializing", "queued"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment order = %v, want %v", got, want)
		}
	}
}

func TestLogDefault(t *testing.T) {
	rows := func(kind string, finished bool, st events.Status) bool {
		v := liveView{Stacks: []events.StackState{{Path: "x", Status: st}}}
		m := buildLiveModel(v, kind, finished, time.Now())
		return m.Groups[0].Stacks[0].LogDefault
	}
	if rows("plan", true, events.StatusSafe) {
		t.Error("finished plan stack should default to Result (LogDefault=false)")
	}
	if !rows("plan", false, events.StatusRunning) {
		t.Error("running stack should default to Log")
	}
	if !rows("apply", true, events.StatusSafe) {
		t.Error("apply stack should default to Log (Result is empty)")
	}
	if !rows("plan", true, events.StatusFailed) {
		t.Error("failed stack should default to Log")
	}
}
