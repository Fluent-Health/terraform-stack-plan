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
