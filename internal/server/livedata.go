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

// phaseOrder is the lifecycle phase progression shown in the timeline.
var phaseOrder = []events.Phase{
	events.PhaseWarming, events.PhaseInitializing, events.PhasePlanning, events.PhaseApplying, events.PhaseVerifying,
}

// phaseTimeline returns the ordered phases with state relative to cur: phases
// before cur are "done", cur is "active", later phases are "todo". An unknown or
// empty cur (not in phaseOrder) leaves every phase "todo".
func phaseTimeline(cur events.Phase) []phaseStep {
	curIdx := -1
	for i, p := range phaseOrder {
		if p == cur {
			curIdx = i
			break
		}
	}
	steps := make([]phaseStep, 0, len(phaseOrder))
	for i, p := range phaseOrder {
		state := "todo"
		if curIdx >= 0 {
			switch {
			case i < curIdx:
				state = "done"
			case i == curIdx:
				state = "active"
			}
		}
		steps = append(steps, phaseStep{Name: string(p), State: state})
	}
	return steps
}
