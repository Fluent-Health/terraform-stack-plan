package server

import (
	"regexp"
	"sort"
	"strconv"

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

// groupByProject folds stacks by their Project (the Google project / grouping
// target, backfilled at finalize). Stacks with no Project (pre-finalize or
// unprojected) fall into a trailing "—" bucket. Order: projects alphabetical,
// then the ungrouped bucket last; stack order within a group is preserved.
func groupByProject(stacks []events.StackState) []stackGroup {
	const ungrouped = "—"
	byName := map[string][]events.StackState{}
	var order []string
	for _, s := range stacks {
		k := s.Project
		if k == "" {
			k = ungrouped
		}
		if _, ok := byName[k]; !ok {
			order = append(order, k)
		}
		byName[k] = append(byName[k], s)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i] == ungrouped {
			return false
		}
		if order[j] == ungrouped {
			return true
		}
		return order[i] < order[j]
	})
	groups := make([]stackGroup, 0, len(order))
	for _, n := range order {
		groups = append(groups, stackGroup{Name: n, Stacks: byName[n]})
	}
	return groups
}

// progSeg is one segment of the progress bar: one stack, flex-sized by its
// mutating-op total (min 1 so a 0-op stack still shows a sliver), colored by the
// stack's CURRENT run-state — so the bar tracks live progress (each segment turns
// its state colour as the stack advances), rather than a static op-kind breakdown.
type progSeg struct {
	Flex     int
	StateCSS string // queued|initializing|planning|planned|applying|moving|applied|blocked|skipped|failed
}

func progressSegments(stacks []events.StackState, kind string, phase events.Phase) []progSeg {
	segs := make([]progSeg, 0, len(stacks))
	for _, s := range stacks {
		flex := 1
		if s.Counts != nil {
			if t := s.Counts.Add + s.Counts.Change + s.Counts.Destroy + s.Counts.Replace + s.Counts.Move; t > 0 {
				flex = t
			}
		}
		segs = append(segs, progSeg{Flex: flex, StateCSS: displayState(s.Status, kind, phase).CSS})
	}
	return segs
}

// iamCount is the number of stacks flagged with the gating "iam" category.
func iamCount(stacks []events.StackState) int {
	n := 0
	for _, s := range stacks {
		for _, c := range s.Categories {
			if c.Name == "iam" {
				n++
				break
			}
		}
	}
	return n
}

// riskTag is a per-stack risk chip (iam / destructive).
type riskTag struct {
	Label string
	CSS   string // iam | danger
}

func riskTags(s events.StackState) []riskTag {
	var tags []riskTag
	for _, c := range s.Categories {
		switch c.Name {
		case "iam":
			tags = append(tags, riskTag{"⚿ IAM", "iam"})
		case "destructive":
			tags = append(tags, riskTag{"⚠ destructive", "danger"})
		}
	}
	return tags
}

// verdict is the aggregate op tally across an execution's stacks, for the
// verdict band + blast-radius bar.
type verdict struct {
	Add, Change, Destroy, Replace, Move, Import, Forget int
	TotalOps                                            int // Add+Change+Destroy+Replace
}

func aggregateVerdict(stacks []events.StackState) verdict {
	var v verdict
	for _, s := range stacks {
		if s.Counts == nil {
			continue
		}
		c := s.Counts
		v.Add += c.Add
		v.Change += c.Change
		v.Destroy += c.Destroy
		v.Replace += c.Replace
		v.Move += c.Move
		v.Import += c.Import
		v.Forget += c.Forget
	}
	v.TotalOps = v.Add + v.Change + v.Destroy + v.Replace
	return v
}

// stateDisplay is a per-stack status rendered for the UI: a human label + a
// css-state slug (drives the per-state label color class state-<CSS>).
type stateDisplay struct {
	Label string
	CSS   string
}

// applyStarted reports whether the execution has entered the real apply (or
// post-apply verify) — i.e. past the pre-apply re-plan/classify pass. Before
// that, an apply page's per-stack "running"/"planned" ticks come from re-planning,
// not applying, so they read as "preparing"/"prepared".
func applyStarted(p events.Phase) bool {
	return p == events.PhaseApplying || p == events.PhaseVerifying
}

// displayState maps the protocol Status (+ plan/apply kind + execution phase) to
// the viewer's richer per-state label. Plan and apply share Status values but read
// differently; an apply also reads differently before vs after the real apply
// begins (the pre-apply re-plan pass is "preparing"/"prepared", not "applying").
func displayState(st events.Status, kind string, phase events.Phase) stateDisplay {
	applying := kind == "apply" && applyStarted(phase)
	switch st {
	case events.StatusPending:
		return stateDisplay{"queued", "queued"}
	case events.StatusInitializing:
		return stateDisplay{"initializing", "initializing"}
	case events.StatusInitialized:
		return stateDisplay{"initialized", "initialized"}
	case events.StatusRunning:
		if kind == "apply" {
			if applying {
				return stateDisplay{"applying", "applying"}
			}
			return stateDisplay{"preparing", "preparing"}
		}
		return stateDisplay{"planning", "planning"}
	case events.StatusPlanned:
		if kind == "apply" {
			if applying {
				return stateDisplay{"queued", "queued"}
			}
			return stateDisplay{"prepared", "prepared"}
		}
		return stateDisplay{"planned", "planned"}
	case events.StatusMoving:
		return stateDisplay{"moving", "moving"}
	case events.StatusGated:
		return stateDisplay{"blocked", "blocked"}
	case events.StatusSafe:
		if kind == "apply" {
			return stateDisplay{"applied", "applied"}
		}
		return stateDisplay{"planned", "planned"}
	case events.StatusFailed:
		return stateDisplay{"failed", "failed"}
	case events.StatusNochange:
		return stateDisplay{"no changes", "nochange"}
	case events.StatusAborted:
		return stateDisplay{"aborted", "aborted"}
	default:
		return stateDisplay{string(st), "queued"}
	}
}

// opSummary renders a compact per-stack op count — the single dominant kind by
// count (tie order add>change>replace>destroy>move). "" when nil/empty.
func opSummary(c *events.Counts) string {
	if c == nil {
		return ""
	}
	switch {
	case c.Add > 0:
		return "+" + strconv.Itoa(c.Add)
	case c.Change > 0:
		return "~" + strconv.Itoa(c.Change)
	case c.Replace > 0:
		return "±" + strconv.Itoa(c.Replace)
	case c.Destroy > 0:
		return "−" + strconv.Itoa(c.Destroy)
	case c.Move > 0:
		return "↔" + strconv.Itoa(c.Move)
	default:
		return ""
	}
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
	case events.StatusNochange:
		return "badge-ghost"
	case events.StatusAborted:
		return "badge-ghost"
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
			{name: "Warming", phase: events.PhaseWarming},
			{name: "Initializing", phase: events.PhaseInitializing},
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
