package server

import (
	"regexp"
	"sort"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// stackGroup is a set of stacks sharing a group key.
type stackGroup struct {
	Name   string
	Stacks []events.StackState
}

// groupStacksByKey folds stacks into groups by the same key as the group DAG
// (groupKey at the given depth / regexp), group names alphabetically sorted; stack
// order within a group is preserved (the store returns them path-sorted). This
// makes the folding list the DAG's per-stack drill-down.
func groupStacksByKey(stacks []events.StackState, depth int, re *regexp.Regexp) []stackGroup {
	byName := map[string][]events.StackState{}
	var order []string
	for _, s := range stacks {
		k := groupKey(s.Path, depth, re)
		if _, ok := byName[k]; !ok {
			order = append(order, k)
		}
		byName[k] = append(byName[k], s)
	}
	sort.Strings(order)
	groups := make([]stackGroup, 0, len(order))
	for _, n := range order {
		groups = append(groups, stackGroup{Name: n, Stacks: byName[n]})
	}
	return groups
}

// statusBadge maps a per-stack status to a DaisyUI badge class. Unknown statuses
// fall back to a neutral ghost badge.
func statusBadge(s events.Status) string {
	switch s {
	case events.StatusPlanned, events.StatusMoving:
		return "badge-info"
	case events.StatusGated:
		return "badge-warning"
	case events.StatusSafe:
		return "badge-success"
	case events.StatusFailed:
		return "badge-error"
	default: // pending + anything unknown
		return "badge-ghost"
	}
}

// phaseStep is one cell of the lifecycle timeline.
type phaseStep struct {
	Name  string
	State string // "done" | "active" | "todo"
}

// phaseTimeline returns the context-appropriate ordered phase steps.
//
// kind is derived from the execution's Context field: "apply" (starts with
// "apply") vs "plan" (anything else). finished signals that the execution has
// concluded (report present for plan, terminal Status for apply).
//
// Plan kind:  Plan → Report
// Apply kind: Apply → Verify
//
// Step state: done = completed, active = current phase, todo = future.
// When finished, all steps are "done" (nothing left stuck "active").
func phaseTimeline(kind string, cur events.Phase, finished bool) []phaseStep {
	type spec struct {
		name  string
		phase events.Phase // the runner phase that drives this step
	}
	var specs []spec
	if kind == "apply" {
		specs = []spec{
			{name: "Apply", phase: events.PhaseApplying},
			{name: "Verify", phase: events.PhaseVerifying},
		}
	} else {
		specs = []spec{
			{name: "Plan", phase: events.PhasePlanning},
			{name: "Report", phase: ""}, // synthetic: no phase emitted; done when report lands
		}
	}

	// Find which step is currently active (first whose phase matches cur, or the
	// last step that has been passed).
	curIdx := -1
	for i, s := range specs {
		if s.phase != "" && s.phase == cur {
			curIdx = i
			break
		}
	}

	steps := make([]phaseStep, 0, len(specs))
	for i, s := range specs {
		var state string
		switch {
		case finished:
			state = "done"
		case curIdx >= 0 && i < curIdx:
			state = "done"
		case curIdx >= 0 && i == curIdx:
			state = "active"
		default:
			state = "todo"
		}
		steps = append(steps, phaseStep{Name: s.name, State: state})
	}
	return steps
}
