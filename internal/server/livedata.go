package server

import (
	"sort"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// stackGroup is a set of stacks sharing a Project (the grouping/target key).
type stackGroup struct {
	Name   string
	Stacks []events.StackState
}

// groupStacks groups stacks by Project: named projects first (alphabetical),
// then the empty-project stacks last under "(ungrouped)". Stack order within a
// group is preserved (the store returns them sorted by path).
func groupStacks(stacks []events.StackState) []stackGroup {
	const ungrouped = "(ungrouped)"
	byName := map[string][]events.StackState{}
	for _, s := range stacks {
		name := s.Project
		if name == "" {
			name = ungrouped
		}
		byName[name] = append(byName[name], s)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		if n != ungrouped {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if _, ok := byName[ungrouped]; ok {
		names = append(names, ungrouped)
	}
	groups := make([]stackGroup, 0, len(names))
	for _, n := range names {
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
	events.PhaseWarming, events.PhaseInitializing, events.PhasePlanning, events.PhaseApplying,
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
