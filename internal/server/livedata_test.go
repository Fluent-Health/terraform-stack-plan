package server

import (
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

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
		return buildLiveModel(v, "apply", finished, nil, time.Now()).PhaseLabel
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

func TestLogDefault(t *testing.T) {
	rows := func(kind string, finished bool, st events.Status) bool {
		v := liveView{Stacks: []events.StackState{{Path: "x", Status: st}}}
		m := buildLiveModel(v, kind, finished, nil, time.Now())
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
