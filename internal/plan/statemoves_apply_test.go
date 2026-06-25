package plan

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemoves"
)

func TestApplyStateMoves_reclassifiesModuleChildrenAsMoves(t *testing.T) {
	rs := RawStack{
		Counts: model.Counts{Add: 3},
		Changes: []RawChange{
			{Address: "module.content_library.google_project_iam_member.x", Action: model.ActionAdd, Actions: []string{"create"}},
			{Address: "module.content_library.google_cloudbuild_trigger.t", Action: model.ActionAdd, Actions: []string{"create"}},
			{Address: "module.other.google_project_iam_member.y", Action: model.ActionAdd, Actions: []string{"create"}},
		},
	}
	n := rs.ApplyStateMoves(statemoves.Set{"module.content_library": true}) // module-level target
	if n != 2 {
		t.Fatalf("reclassified = %d, want 2 (both content_library children)", n)
	}
	if rs.Counts.Add != 1 || rs.Counts.Move != 2 {
		t.Fatalf("counts = Add:%d Move:%d, want Add:1 Move:2", rs.Counts.Add, rs.Counts.Move)
	}
	for _, c := range rs.Changes[:2] {
		if c.Action != model.ActionNoop || !c.Moved {
			t.Fatalf("%s: Action=%q Moved=%v, want noop+moved", c.Address, c.Action, c.Moved)
		}
	}
	if c := rs.Changes[2]; c.Action != model.ActionAdd || c.Moved {
		t.Fatalf("module.other must stay a create, got Action=%q Moved=%v", c.Action, c.Moved)
	}
}

func TestApplyStateMoves_emptyTargets_isNoop(t *testing.T) {
	rs := RawStack{
		Counts:  model.Counts{Add: 1},
		Changes: []RawChange{{Address: "module.a.r", Action: model.ActionAdd}},
	}
	if n := rs.ApplyStateMoves(statemoves.Set{}); n != 0 {
		t.Fatalf("empty targets must reclassify nothing, got %d", n)
	}
	if rs.Counts.Add != 1 || rs.Counts.Move != 0 {
		t.Fatalf("counts must be unchanged, got Add:%d Move:%d", rs.Counts.Add, rs.Counts.Move)
	}
}

func TestApplyStateMoves_leavesInStackMovesAlone(t *testing.T) {
	// An in-stack `moved` is already Moved; the overlay must not double-count it.
	rs := RawStack{
		Counts:  model.Counts{Move: 1},
		Changes: []RawChange{{Address: "module.content_library.r", Action: model.ActionNoop, Moved: true, PreviousAddress: "module.old.r"}},
	}
	if n := rs.ApplyStateMoves(statemoves.Set{"module.content_library": true}); n != 0 {
		t.Fatalf("in-stack move must be skipped, got %d", n)
	}
	if rs.Counts.Move != 1 {
		t.Fatalf("Move count must stay 1, got %d", rs.Counts.Move)
	}
}

func TestApplyStateMoves_reclassifiesDestroyWithPreviousAddress(t *testing.T) {
	// A `moved {}` block in the same plan renames module.agent → module.agent[0],
	// setting PreviousAddress and Moved=true on the resource. count=0 then
	// destroys it. The source-side cross-state move must still be reclassified
	// even though Moved is already true — only ActionNoop means "pure in-stack
	// rename with nothing left to reclassify".
	rs := RawStack{
		Counts: model.Counts{Destroy: 2, Move: 1},
		Changes: []RawChange{
			// Pure in-stack rename (no-op): must NOT be reclassified.
			{Address: "module.agent[0].r", Action: model.ActionNoop, Moved: true, PreviousAddress: "module.agent.r"},
			// Destroy with PreviousAddress: IS a cross-state move target — must be reclassified.
			{Address: "module.agent[0].google_project_iam_member.x", Action: model.ActionDestroy, Moved: true, PreviousAddress: "module.agent.google_project_iam_member.x"},
			// Another destroy, same pattern.
			{Address: "module.agent[0].google_service_account.main", Action: model.ActionDestroy, Moved: true, PreviousAddress: "module.agent.google_service_account.main"},
		},
	}
	targets := statemoves.Set{
		"module.agent[0].google_project_iam_member.x": true,
		"module.agent[0].google_service_account.main": true,
	}
	n := rs.ApplyStateMoves(targets)
	if n != 2 {
		t.Fatalf("reclassified = %d, want 2", n)
	}
	if rs.Counts.Destroy != 0 || rs.Counts.Move != 3 {
		t.Fatalf("counts = Destroy:%d Move:%d, want Destroy:0 Move:3", rs.Counts.Destroy, rs.Counts.Move)
	}
	// The pure in-stack rename must be untouched.
	if c := rs.Changes[0]; c.Action != model.ActionNoop || c.PreviousAddress != "module.agent.r" {
		t.Fatalf("in-stack noop must be unchanged: Action=%q PreviousAddress=%q", c.Action, c.PreviousAddress)
	}
	// The destroys must now be noop+moved with no attrs.
	for _, c := range rs.Changes[1:] {
		if c.Action != model.ActionNoop || !c.Moved || c.Attrs != nil {
			t.Fatalf("%s: Action=%q Moved=%v Attrs=%v, want noop+moved+nil-attrs", c.Address, c.Action, c.Moved, c.Attrs)
		}
	}
}
