package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// canonicalTemplate is the fixed lifecycle backbone: it orders unobserved
// (pending) segments and carries the human labels. Observed phases render in
// real (timestamp) order; only template keys never observed are appended as
// pending. "approve" is synthetic (no runner phase emits it).
var canonicalTemplate = []struct{ Key, Label, Context string }{
	{"prepare", "prepare", "plan"},
	{"init", "init", "plan"},
	{"moves", "state moves", "apply"},
	{"plan", "plan", "plan"},
	{"classify", "classify", "plan"},
	{"report", "report", "plan"},
	{"approve", "approve", "gate"},
	{"apply", "apply", "apply"},
	{"verify", "verify", "verify"},
}

// canonicalPhaseKey maps a raw runner phase string onto a template key. ok is
// false for phases with no template home (linting, claim, or any future phase)
// — those pass through as generic "working" segments keyed by their raw name.
func canonicalPhaseKey(phase string) (key, label string, ok bool) {
	switch phase {
	case "warming", "image":
		return "prepare", "prepare", true
	case "initializing":
		return "init", "init", true
	case "moves":
		return "moves", "state moves", true
	case "planning":
		return "plan", "plan", true
	case "classify":
		return "classify", "classify", true
	case "report":
		return "report", "report", true
	case "applying":
		return "apply", "apply", true
	case "testing", "verifying":
		return "verify", "verify", true
	}
	return "", "", false
}

func rollupCounts(g events.Graph) events.Counts {
	var c events.Counts
	for _, s := range g.Stacks {
		if s.Counts == nil {
			continue
		}
		c.Add += s.Counts.Add
		c.Change += s.Counts.Change
		c.Destroy += s.Counts.Destroy
		c.Replace += s.Counts.Replace
		c.Move += s.Counts.Move
	}
	return c
}

// countsSummary renders "+a ~b ±c −d ↔e" (same glyphs as the tier-panel
// per-stack label), omitting zero kinds. "" when all zero.
func countsSummary(c events.Counts) string {
	var p []string
	if c.Add != 0 {
		p = append(p, fmt.Sprintf("+%d", c.Add))
	}
	if c.Change != 0 {
		p = append(p, fmt.Sprintf("~%d", c.Change))
	}
	if c.Replace != 0 {
		p = append(p, fmt.Sprintf("±%d", c.Replace))
	}
	if c.Destroy != 0 {
		p = append(p, fmt.Sprintf("−%d", c.Destroy))
	}
	if c.Move != 0 {
		p = append(p, fmt.Sprintf("↔%d", c.Move))
	}
	return strings.Join(p, " ")
}

// gateSummary is the derived approve/apply view of a PR/tier's gate state.
type gateSummary struct {
	count        int    // number of recorded gate targets
	approveState string // done | now | pending | failed | ""  ("" == no gates)
	applyWait    string // e.g. "waits on approval"; "" when nothing pending
}

// deriveGateSummary reads the gate_targets projection. AWAITING (no human
// action taken yet) keeps approve "pending"; ACTIVATING (mid-grant) is "now";
// DENIED/REVOKED marks approve failed; all ACTIVE marks approve done. No
// gates → approveState "".
func deriveGateSummary(targets []store.GateTarget) gateSummary {
	gs := gateSummary{count: len(targets)}
	if len(targets) == 0 {
		return gs
	}
	allActive := true
	blocked := false
	activating := false
	for _, t := range targets {
		switch t.State {
		case "ACTIVE":
		case "DENIED", "REVOKED":
			blocked = true
			allActive = false
		case "ACTIVATING":
			activating = true
			allActive = false
		default: // AWAITING or any other not-yet-started state
			allActive = false
		}
	}
	switch {
	case blocked:
		gs.approveState = "failed"
		gs.applyWait = "blocked on a denied/revoked approval"
	case allActive:
		gs.approveState = "done"
	case activating:
		gs.approveState = "now"
		gs.applyWait = "waits on approval"
	default:
		gs.approveState = "pending"
		gs.applyWait = "waits on approval"
	}
	return gs
}

func execContextKind(statusContext string) string {
	head := strings.SplitN(statusContext, "/", 2)[0]
	switch head {
	case "plan", "apply", "verify":
		return head
	}
	return "other"
}

func (a *App) handleGetLifecycle(w http.ResponseWriter, _ *http.Request, params api.GetLifecycleParams) {
	pr := params.Pr
	execs, err := store.ListExecutionsForPR(a.db, pr)
	if err != nil {
		http.Error(w, "list executions", http.StatusInternalServerError)
		return
	}
	env := a.cfg.Environment
	// Non-superseded executions for THIS tier only.
	var live []store.Execution
	for _, e := range execs {
		if e.SupersededBy != "" || e.Environment != env {
			continue
		}
		live = append(live, e)
	}

	phasesByExec := map[string][]store.PhaseRow{}
	var planGraph, applyGraph events.Graph
	for _, e := range live {
		rows, perr := store.PhasesFor(a.db, e.ID)
		if perr != nil {
			http.Error(w, "load phases", http.StatusInternalServerError)
			return
		}
		phasesByExec[e.ID] = rows
		switch execContextKind(e.StatusContext) {
		case "plan":
			if g, gerr := store.LoadGraph(a.db, e.ID); gerr == nil {
				planGraph = g
			}
		case "apply":
			if g, gerr := store.LoadGraph(a.db, e.ID); gerr == nil {
				applyGraph = g
			}
		}
	}
	targets, terr := store.TargetsFor(a.db, pr, env)
	if terr != nil {
		targets = []store.GateTarget{}
	}

	out := lifecycleFold(live, phasesByExec, rollupCounts(planGraph), rollupCounts(applyGraph), deriveGateSummary(targets))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// lifecycleFold is the pure fold: observed phases in time order + unobserved
// template phases as pending, with derived per-phase result.
func lifecycleFold(execs []store.Execution, phasesByExec map[string][]store.PhaseRow, planCounts, applyCounts events.Counts, gate gateSummary) []api.LifecyclePhase {
	failedByExec := map[string]bool{}
	succeededByExec := map[string]bool{}
	ctxByExec := map[string]string{}
	for _, e := range execs {
		failedByExec[e.ID] = e.Status == "failure"
		succeededByExec[e.ID] = e.Status == "success"
		ctxByExec[e.ID] = execContextKind(e.StatusContext)
	}

	// Flatten and sort every recorded phase row by time, then execution id for
	// a stable tiebreak on identical timestamps.
	type flat struct {
		execID string
		row    store.PhaseRow
	}
	var all []flat
	for id, rows := range phasesByExec {
		for _, r := range rows {
			all = append(all, flat{execID: id, row: r})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].row.At.Equal(all[j].row.At) {
			return all[i].execID < all[j].execID
		}
		return all[i].row.At.Before(all[j].row.At)
	})

	out := make([]api.LifecyclePhase, 0, len(all)+len(canonicalTemplate))
	observed := map[string]bool{}
	for i, f := range all {
		key, label, ok := canonicalPhaseKey(f.row.Phase)
		if !ok {
			key, label = f.row.Phase, f.row.Phase
		}
		observed[key] = true
		ctx := ctxByExec[f.execID]
		state := "done"
		if i == len(all)-1 {
			switch {
			case failedByExec[f.execID]:
				state = "failed"
			case succeededByExec[f.execID]:
				state = "done"
			default:
				state = "now"
			}
		}
		lp := api.LifecyclePhase{Key: key, Label: label, State: state, Context: ptr(ctx)}
		start := f.row.At
		lp.StartedAt = &start
		if i+1 < len(all) {
			end := all[i+1].row.At
			lp.EndedAt = &end
		}
		if r := deriveResult(key, ctx, planCounts, applyCounts, gate, state); r != "" {
			lp.Result = ptr(r)
		}
		out = append(out, lp)
	}

	// Append template phases never observed, in template order, as pending.
	// The synthetic "approve" phase adopts the gate-derived state.
	for _, t := range canonicalTemplate {
		if observed[t.Key] {
			continue
		}
		if t.Key == "approve" && gate.approveState == "" {
			continue // no gates → no approve segment
		}
		state := "pending"
		if t.Key == "approve" && gate.approveState != "" {
			state = gate.approveState
		}
		lp := api.LifecyclePhase{Key: t.Key, Label: t.Label, State: state, Context: ptr(t.Context)}
		if r := deriveResult(t.Key, t.Context, planCounts, applyCounts, gate, state); r != "" {
			lp.Result = ptr(r)
		}
		out = append(out, lp)
	}
	return out
}

// deriveResult computes the per-phase summary. Never stored — always derived
// from the graph rollup counts and gate state.
func deriveResult(key, ctx string, planCounts, applyCounts events.Counts, gate gateSummary, state string) string {
	switch key {
	case "plan", "report":
		if ctx == "apply" {
			return countsSummary(applyCounts)
		}
		return countsSummary(planCounts)
	case "classify":
		switch gate.count {
		case 0:
			return "no gates"
		case 1:
			return "1 gate required"
		default:
			return fmt.Sprintf("%d gates required", gate.count)
		}
	case "approve":
		if gate.count > 0 && state != "done" {
			return gate.applyWait
		}
		return ""
	case "apply":
		if state == "pending" && gate.applyWait != "" {
			return gate.applyWait
		}
		return ""
	}
	return ""
}
