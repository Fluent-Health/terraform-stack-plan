package plan

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/statemoves"
)

// ApplyStateMoves reclassifies every change whose address is a pending
// cross-state move-target (from the --state-moves manifest) as a MOVE rather
// than the create/update/destroy terraform plans it as. A cross-state move lands
// the resource in a fresh destination state, so terraform shows it as a create
// (dest) or destroy (source) with no PreviousAddress; --state-moves is the
// out-of-band knowledge that it is really a relocation. After this overlay such
// a change is indistinguishable from an in-stack `moved`: its mutating count is
// removed (it makes no apply-time provider write), Counts.Move is incremented,
// Action becomes Noop (so classification skips it — no IAM gate) and Moved is
// set (so it renders with the move glyph, not a create). Returns the number of
// changes reclassified so the caller can surface a non-gating "move" category.
//
// An empty target set is a no-op (the fail-safe when --state-moves is absent).
//
// Note on the Moved guard: a resource in the SOURCE stack can appear with
// Moved=true when a same-plan `moved {}` block renames it (e.g. module.agent →
// module.agent[0]) while simultaneously count=0 destroys the new address. In
// that case Action is ActionDestroy, not ActionNoop, so we must still reclassify
// it. We only skip when Action is already ActionNoop — meaning it's a pure
// same-stack rename with no real provider write.
func (rs *RawStack) ApplyStateMoves(targets statemoves.Set) int {
	if targets.Len() == 0 {
		return 0
	}
	n := 0
	for i := range rs.Changes {
		c := &rs.Changes[i]
		if c.Action == model.ActionNoop || c.Imported || !targets.Covers(c.Address) {
			continue // already a pure state op (in-stack move/import) or not a target
		}
		switch c.Action {
		case model.ActionAdd:
			rs.Counts.Add--
		case model.ActionChange:
			rs.Counts.Change--
		case model.ActionDestroy:
			rs.Counts.Destroy--
		case model.ActionReplace:
			rs.Counts.Replace--
		}
		c.Action = model.ActionNoop
		c.Moved = true
		c.Attrs = nil // a relocation shows no attribute diff
		rs.Counts.Move++
		n++
	}
	return n
}
