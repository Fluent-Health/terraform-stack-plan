package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// canonicalTemplate is the fixed lifecycle backbone: it orders segments (via
// rank) and carries the human labels. Only template keys never observed AND
// relevant to the PR's current stage are appended as pending — a plan-only PR
// shows no apply-side noise. "approve" is synthetic (no runner phase emits it).
var canonicalTemplate = []struct{ Key, Label, Context string }{
	{"prepare", "prepare", "plan"},
	{"init", "init", "plan"},
	{"plan", "plan", "plan"},
	{"classify", "classify", "plan"},
	{"report", "report", "plan"},
	{"approve", "approve", "gate"},
	{"moves", "state moves", "apply"},
	{"apply", "apply", "apply"},
	{"verify", "verify", "verify"},
}

// canonicalRank orders segments for display: canonical keys get template
// position ×10; passthrough keys (linting, claim, …) slot midway after the
// canonical key they were observed after.
var canonicalRank = func() map[string]int {
	m := map[string]int{}
	for i, t := range canonicalTemplate {
		m[t.Key] = i * 10
	}
	return m
}()

// segmentForPhase maps one raw runner phase onto its display segment, in the
// context of the execution that emitted it. An apply run's own housekeeping
// phases (warming/initializing/classify/report) belong INSIDE the apply
// segment — surfaced as a sub-phase detail — never to the plan-side segments,
// so a running apply cannot re-activate the already-done plan side. ok is
// false for phases with no segment home (linting, claim, or any future phase)
// — those pass through as generic segments keyed by their raw name.
func segmentForPhase(ctx, phase string) (key, label, detail string, ok bool) {
	switch ctx {
	case "apply":
		switch phase {
		case "warming", "image":
			return "apply", "apply", "warming caches", true
		case "initializing":
			return "apply", "apply", "initializing stacks", true
		case "moves":
			return "moves", "state moves", "", true
		case "applying":
			return "apply", "apply", "", true
		case "classify":
			return "apply", "apply", "classifying results", true
		case "report":
			return "apply", "apply", "writing report", true
		case "testing", "verifying":
			return "verify", "verify", "", true
		}
		return "", "", "", false
	case "verify":
		switch phase {
		case "warming", "image":
			return "verify", "verify", "preparing", true
		case "testing", "verifying":
			return "verify", "verify", "", true
		}
		return "", "", "", false
	}
	// plan context (and anything unrecognized).
	switch phase {
	case "warming", "image":
		return "prepare", "prepare", "", true
	case "initializing":
		return "init", "init", "", true
	case "moves":
		return "moves", "state moves", "", true
	case "planning":
		return "plan", "plan", "", true
	case "classify":
		return "classify", "classify", "", true
	case "report":
		return "report", "report", "", true
	case "applying":
		return "apply", "apply", "", true
	case "testing", "verifying":
		return "verify", "verify", "", true
	}
	return "", "", "", false
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
	// Scope to the PR's current head SHA first: apply runs are never superseded
	// and gate state is SHA-agnostic, so after a new push the prior cycle's
	// apply-side executions stay "live". Folding them alongside the fresh plan
	// would paint a stale, mostly-done "applying" bar for a run that has barely
	// started; scoping drops the previous cycle the moment the new run registers.
	live = scopeToHeadSHA(live)
	// One execution per status context: supersession only links same-SHA reruns,
	// so a same-SHA rerun leaves its predecessor "live" — folding them all would
	// repeat the whole plan-side of the bar once per rerun. The newest run per
	// context (plan/apply/verify each keep their own) is the PR's current lifecycle.
	live = newestPerStatusContext(live)

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

	out := lifecycleFold(live, phasesByExec, planGraph, applyGraph, deriveGateSummary(targets))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// scopeToHeadSHA keeps only executions for the PR's current head SHA — the SHA
// of the newest execution (tie-broken by ID). This drops a superseded push's
// still-"live" apply-side executions once the new SHA's run registers, so the
// bar reflects one cycle at a time. Empty input (or execs with a blank SHA) is
// returned unchanged.
func scopeToHeadSHA(execs []store.Execution) []store.Execution {
	var head store.Execution
	found := false
	for _, e := range execs {
		if !found || e.CreatedAt.After(head.CreatedAt) ||
			(e.CreatedAt.Equal(head.CreatedAt) && e.ID > head.ID) {
			head, found = e, true
		}
	}
	if !found {
		return execs
	}
	out := make([]store.Execution, 0, len(execs))
	for _, e := range execs {
		if e.SHA == head.SHA {
			out = append(out, e)
		}
	}
	return out
}

// newestPerStatusContext keeps only the newest execution per status context,
// ordered oldest-context-first for a deterministic fold.
func newestPerStatusContext(execs []store.Execution) []store.Execution {
	best := map[string]store.Execution{}
	for _, e := range execs {
		cur, ok := best[e.StatusContext]
		if !ok || e.CreatedAt.After(cur.CreatedAt) || (e.CreatedAt.Equal(cur.CreatedAt) && e.ID > cur.ID) {
			best[e.StatusContext] = e
		}
	}
	out := make([]store.Execution, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// lifecycleFold is the pure fold: observed phases mapped context-aware onto
// display segments, plus unobserved-but-relevant template segments as pending,
// all ordered by canonical rank. Each segment key renders at most once: a
// re-observed key (a run's second `warming` tick, or an apply's housekeeping
// phases all mapping into the apply segment) coalesces into its first segment,
// keeping the first start, adopting the latest end/state/detail. The "now"
// segment carries within-segment progress (k of N stacks) when the graph
// knows it.
func lifecycleFold(execs []store.Execution, phasesByExec map[string][]store.PhaseRow, planGraph, applyGraph events.Graph, gate gateSummary) []api.LifecyclePhase {
	planCounts, applyCounts := rollupCounts(planGraph), rollupCounts(applyGraph)
	failedByExec := map[string]bool{}
	succeededByExec := map[string]bool{}
	ctxByExec := map[string]string{}
	hasCtx := map[string]bool{}
	applySucceeded := false
	for _, e := range execs {
		failedByExec[e.ID] = e.Status == "failure"
		succeededByExec[e.ID] = e.Status == "success"
		k := execContextKind(e.StatusContext)
		ctxByExec[e.ID] = k
		hasCtx[k] = true
		if k == "apply" && e.Status == "success" {
			applySucceeded = true
		}
	}

	// Flatten every recorded phase row and sort by time. Ties (second-resolution
	// timestamps) keep the flattening order: executions oldest-first (the
	// caller passes them sorted by created_at), rows in recorded order within
	// each execution — so a plan's rows never interleave into a same-second
	// apply's, and vice versa.
	type flat struct {
		execID string
		row    store.PhaseRow
	}
	var all []flat
	for _, e := range execs {
		for _, r := range phasesByExec[e.ID] {
			all = append(all, flat{execID: e.ID, row: r})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].row.At.Before(all[j].row.At)
	})

	// A segment is "now" when it is its execution's LATEST phase and that
	// execution is still running — per execution, not globally, so a finished
	// plan's report can never mask a running apply's current phase (or vice
	// versa) however same-second rows interleave.
	lastRowOf := map[string]int{}
	for i, f := range all {
		lastRowOf[f.execID] = i
	}

	out := make([]api.LifecyclePhase, 0, len(all)+len(canonicalTemplate))
	ranks := make([]int, 0, cap(out)) // parallel to out
	segAt := map[string]int{}         // segment key → index in out
	lastCanonicalRank := -10
	for i, f := range all {
		ctx := ctxByExec[f.execID]
		key, label, detail, ok := segmentForPhase(ctx, f.row.Phase)
		if !ok {
			key, label = f.row.Phase, f.row.Phase
		}
		state := "done"
		if i == lastRowOf[f.execID] {
			switch {
			case failedByExec[f.execID]:
				state = "failed"
			case succeededByExec[f.execID]:
				state = "done"
			default:
				state = "now"
			}
		}
		start := f.row.At
		var endedAt *time.Time
		if i+1 < len(all) {
			e := all[i+1].row.At
			endedAt = &e
		}
		// The sub-phase detail only matters while it is what's happening now.
		var detailPtr *string
		if state == "now" && detail != "" {
			detailPtr = ptr(detail)
		}
		if j, seen := segAt[key]; seen {
			// Re-observation: the same segment progressing — extend it instead
			// of repeating it in the bar.
			out[j].State = state
			out[j].EndedAt = endedAt
			out[j].Detail = detailPtr
			if r := deriveResult(key, ctx, planCounts, applyCounts, gate, state); r != "" {
				out[j].Result = ptr(r)
			}
			continue
		}
		lp := api.LifecyclePhase{Key: key, Label: label, State: state, Context: ptr(ctx), Detail: detailPtr}
		lp.StartedAt = &start
		lp.EndedAt = endedAt
		if r := deriveResult(key, ctx, planCounts, applyCounts, gate, state); r != "" {
			lp.Result = ptr(r)
		}
		rank, canonical := canonicalRank[key]
		if !canonical {
			rank = lastCanonicalRank + 5 // passthrough slots after its predecessor
		} else {
			lastCanonicalRank = rank
		}
		segAt[key] = len(out)
		out = append(out, lp)
		ranks = append(ranks, rank)
	}

	// Append template segments never observed, as pending — but only when
	// relevant to the PR's stage: a plan-only PR renders no apply-side noise,
	// `moves` only shows when the plan actually detected state moves, and
	// `init` only ever renders observed (plan runs narrate init per-stack, not
	// as a run phase). The synthetic "approve" segment adopts the gate state.
	movesKnown := hasMovingStacks(planGraph) || hasMovingStacks(applyGraph)
	for _, t := range canonicalTemplate {
		if _, seen := segAt[t.Key]; seen {
			continue
		}
		switch t.Key {
		case "init":
			continue
		case "prepare", "plan", "classify", "report":
			if !hasCtx["plan"] {
				continue
			}
		case "approve":
			if gate.approveState == "" {
				continue
			}
		case "moves":
			if !hasCtx["apply"] || !movesKnown {
				continue
			}
		case "apply":
			if !hasCtx["apply"] {
				continue
			}
		case "verify":
			if !hasCtx["apply"] && !hasCtx["verify"] {
				continue
			}
		}
		state := "pending"
		if t.Key == "approve" {
			state = gate.approveState
		}
		// A never-observed verify on a terminally-succeeded apply means the run
		// is complete and these components carry no verify step — render it done
		// (green), not a perpetually-pending grey segment.
		if t.Key == "verify" && applySucceeded {
			state = "done"
		}
		lp := api.LifecyclePhase{Key: t.Key, Label: t.Label, State: state, Context: ptr(t.Context)}
		if r := deriveResult(t.Key, t.Context, planCounts, applyCounts, gate, state); r != "" {
			lp.Result = ptr(r)
		}
		out = append(out, lp)
		ranks = append(ranks, canonicalRank[t.Key])
	}

	// Canonical display order (stable: ties keep observation order).
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return ranks[idx[a]] < ranks[idx[b]] })
	sorted := make([]api.LifecyclePhase, len(out))
	for i, j := range idx {
		sorted[i] = out[j]
	}

	// Attach within-segment progress to the "now" segment when the graph
	// carries real per-stack completion for it.
	for i := range sorted {
		if sorted[i].State != "now" {
			continue
		}
		if pct, ok := segmentProgress(sorted[i].Key, planGraph, applyGraph); ok {
			sorted[i].ProgressPct = ptr(pct)
		}
	}
	return sorted
}

func hasMovingStacks(g events.Graph) bool {
	for _, s := range g.Stacks {
		if s.Status == events.StatusMoving {
			return true
		}
	}
	return false
}

// segmentProgress derives a "now" segment's completion from per-stack terminal
// statuses — only for the segments that genuinely tick per stack (plan and
// apply); marker segments (prepare/classify/report/…) have no meaningful k/N.
func segmentProgress(key string, planGraph, applyGraph events.Graph) (int, bool) {
	var g events.Graph
	var terminal func(events.Status) bool
	switch key {
	case "plan":
		g = planGraph
		terminal = func(s events.Status) bool {
			switch s {
			case events.StatusPending, events.StatusRunning, events.StatusInitializing, events.StatusInitialized:
				return false
			}
			return true
		}
	case "apply":
		g = applyGraph
		terminal = func(s events.Status) bool {
			switch s {
			case events.StatusSafe, events.StatusNochange, events.StatusFailed, events.StatusAborted:
				return true
			}
			return false
		}
	default:
		return 0, false
	}
	total := len(g.Stacks)
	if total == 0 {
		return 0, false
	}
	done := 0
	for _, s := range g.Stacks {
		if terminal(s.Status) {
			done++
		}
	}
	return done * 100 / total, true
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
