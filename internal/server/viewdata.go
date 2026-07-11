// Shared view helpers: pure projections of stack state used by the check-run
// rendering (risk tags, op summaries, verdict tallies). The HTML live viewer
// that originally owned them retired in favor of the central UI.
package server

import (
	"strconv"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

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
