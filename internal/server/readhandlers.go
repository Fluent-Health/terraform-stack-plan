package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// Read endpoints for aggregating consumers (the central UI, CLI, agents) —
// clean snake_case shapes defined in api/openapi.yaml, unlike the legacy
// execution read whose PascalCase layout is frozen for wire compatibility.

const (
	defaultExecutionsLimit = 100
	maxExecutionsLimit     = 1000
)

// handleListExecutions serves GET /api/executions: recent executions (newest
// first), optionally filtered to one PR's full timeline.
func (a *App) handleListExecutions(w http.ResponseWriter, _ *http.Request, params api.ListExecutionsParams) {
	limit := defaultExecutionsLimit
	if params.Limit != nil {
		limit = *params.Limit
		if limit < 1 || limit > maxExecutionsLimit {
			http.Error(w, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
	}
	var (
		execs []store.Execution
		err   error
	)
	if params.Pr != nil {
		execs, err = store.ListExecutionsForPR(a.db, *params.Pr)
		if err == nil && len(execs) > limit {
			execs = execs[:limit]
		}
	} else {
		execs, err = store.ListExecutions(a.db, limit)
	}
	if err != nil {
		http.Error(w, "list executions", http.StatusInternalServerError)
		return
	}
	out := make([]api.ExecutionSummary, 0, len(execs))
	for _, e := range execs {
		progressLabel := ""
		if e.ProgressLabel.Valid {
			progressLabel = e.ProgressLabel.String
		} else {
			progressLabel = phaseLabel(events.Phase(e.Phase), 0, 0, 0)
		}
		progressPct := 0
		if e.ProgressPct.Valid {
			progressPct = int(e.ProgressPct.Int64)
		} else {
			var weights []config.PhaseWeight
			if a.cfg.Progress != nil {
				if isApplyContext(e.StatusContext) {
					weights = a.cfg.Progress.Apply
				} else {
					weights = a.cfg.Progress.Plan
				}
			}
			_, _, progressPct = progress(weights, events.Phase(e.Phase), 0, 0, 0)
		}

		out = append(out, api.ExecutionSummary{
			Id:            e.ID,
			Repo:          e.Repo,
			Sha:           e.SHA,
			Pr:            e.PR,
			Environment:   e.Environment,
			Context:       e.StatusContext,
			Status:        e.Status,
			Phase:         e.Phase,
			ProgressLabel: progressLabel,
			ProgressPct:   progressPct,
			CreatedAt:     e.CreatedAt,
			SupersededBy:  e.SupersededBy,
			LogUrl:        e.LogURL,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleListApprovals serves GET /api/approvals: every gate target awaiting
// human action across all PRs, with the PR context and grant resource name.
func (a *App) handleListApprovals(w http.ResponseWriter, _ *http.Request) {
	pending, err := store.PendingApprovals(a.db)
	if err != nil {
		http.Error(w, "list approvals", http.StatusInternalServerError)
		return
	}
	out := make([]api.PendingApproval, 0, len(pending))
	for _, p := range pending {
		out = append(out, api.PendingApproval{
			Pr:          p.PR,
			Environment: p.Environment,
			Repo:        p.Repo,
			Class:       p.Class,
			Target:      p.Target,
			GrantName:   p.GrantName,
			State:       p.State,
			Requester:   p.Requester,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleGetPR serves GET /api/pr/{n}: a PR's identity (as last reported by
// the GitHub webhook, store.GetPRMetaByPR) plus this tier's merge state
// (a.prMergeState). The metadata lookup is by pr alone — NOT the
// repo-keyed store.GetPRMeta — because the webhook writes pr_meta on PR
// open/sync, before any execution exists (executions appear only once the
// runner calls /api/init); deriving repo from "latest execution" first would
// make the meta lookup miss the real row in that window. repo in the
// response prefers the latest execution's repo (env-agnostic, same "repo of
// latest execution" idiom store.PendingApprovals uses) and falls back to
// meta.Repo when there is no execution yet. 404 "unknown pr" only when the
// PR is unknown to this tier entirely: no metadata row AND no execution.
func (a *App) handleGetPR(w http.ResponseWriter, _ *http.Request, pr int) {
	execs, err := store.ListExecutionsForPR(a.db, pr)
	if err != nil {
		http.Error(w, "get pr", http.StatusInternalServerError)
		return
	}
	meta, metaOK, err := store.GetPRMetaByPR(a.db, pr)
	if err != nil {
		http.Error(w, "get pr", http.StatusInternalServerError)
		return
	}
	if !metaOK && len(execs) == 0 {
		http.Error(w, "unknown pr", http.StatusNotFound)
		return
	}
	repo := ""
	if len(execs) > 0 {
		repo = execs[0].Repo
	} else if metaOK {
		repo = meta.Repo
	}
	merge := a.prMergeState(pr)
	out := api.PRView{
		Pr:   pr,
		Repo: repo,
		Merge: api.PRMergeState{
			Environment:     merge.Environment,
			RequiredCheck:   merge.RequiredCheck,
			CheckConclusion: merge.CheckConclusion,
			MergeBlocked:    merge.MergeBlocked,
			Blocker:         merge.Blocker,
		},
	}
	if metaOK {
		out.Meta = &api.PRMeta{
			Title:       meta.Title,
			Body:        meta.Body,
			AuthorLogin: meta.AuthorLogin,
			HeadRef:     meta.HeadRef,
			Url:         meta.URL,
			AutoMerge:   meta.AutoMerge,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleMergeQueue serves GET /api/merge-queue: the repository's live GitHub
// merge queue for its default branch, tier-agnostic. Repo comes from this
// tier's newest execution; no execution, or any GitHub error, degrades to an
// empty queue (still HTTP 200) so the UI simply hides the hero.
func (a *App) handleMergeQueue(w http.ResponseWriter, r *http.Request) {
	out := api.MergeQueue{Entries: []api.MergeQueueEntry{}}
	if execs, err := store.ListExecutions(a.db, 1); err == nil && len(execs) > 0 {
		if res, err := a.gh.MergeQueue(r.Context(), execs[0].Repo); err != nil {
			log.Printf("serve: merge-queue read for %s failed: %v", execs[0].Repo, err)
		} else {
			out.Branch = res.Branch
			for _, e := range res.Entries {
				out.Entries = append(out.Entries, api.MergeQueueEntry{Position: e.Position, Pr: e.PR, State: e.State})
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) handleInspectGrants(w http.ResponseWriter, r *http.Request, params api.InspectGrantsParams) {
	ctx := r.Context()
	query := "SELECT pr, environment, class, target, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,'') FROM gate_targets"
	if params.State != nil && *params.State == "open" {
		query += " WHERE state IN ('AWAITING', 'ACTIVATING', 'ACTIVE')"
	}
	rows, err := a.db.Query(query)
	if err != nil {
		http.Error(w, "query grants", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	grants := []api.InspectGrant{}
	for rows.Next() {
		var g api.InspectGrant
		if err := rows.Scan(&g.Pr, &g.Environment, &g.Class, &g.Target, &g.GrantName, &g.State, &g.Requester); err != nil {
			http.Error(w, "scan grant", http.StatusInternalServerError)
			return
		}
		grants = append(grants, g)
	}

	// Live drift check
	if params.Live != nil && *params.Live == 1 && a.Approval != nil {
		triggered := make(map[string]bool)
		for i := range grants {
			g := &grants[i]
			liveGrants, lerr := a.Approval.ListGrants(ctx, g.Class, g.Target)
			if lerr == nil {
				for _, lg := range liveGrants {
					if lg.Request.PR == g.Pr && lg.Request.Environment == g.Environment {
						actual := string(lg.State)
						if actual != g.State {
							g.DriftDetected = ptr(true)
							g.ActualState = ptr(actual)

							key := fmt.Sprintf("%d|%s", g.Pr, g.Environment)
							if !triggered[key] {
								triggered[key] = true
								// Trigger self-healing nudge
								a.reconcileBackground(g.Pr, g.Environment)
							}
						}
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(grants)
}

func ptr[T any](v T) *T { return &v }

func (a *App) reconcileBackground(pr int, env string) {
	go func() {
		_ = a.shell.tick(context.Background(), pr, env)
	}()
}

func (a *App) handleInspectGate(w http.ResponseWriter, r *http.Request, pr int, env string) {
	streamID := execStreamID(pr, env)

	// Load ChangeSet state
	cs, _, err := a.gateDecider.Load(a.eventStore, streamID)
	if err != nil {
		http.Error(w, "load changeset", http.StatusInternalServerError)
		return
	}

	// Fetch raw events and timestamps in one single query
	rows, err := a.db.Query(`SELECT version, type, occurred_at, data FROM events WHERE stream_id = ? ORDER BY version`, streamID)
	if err != nil {
		http.Error(w, "query events", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type eventRow struct {
		Version    int
		Type       string
		OccurredAt time.Time
		Data       []byte
	}

	var stored []eventRow
	for rows.Next() {
		var er eventRow
		if err := rows.Scan(&er.Version, &er.Type, &er.OccurredAt, &er.Data); err == nil {
			stored = append(stored, er)
		}
	}

	var reasons []api.InspectGateReason
	// Replay and record changes
	tempCS := a.gateDecider.Initial()
	for _, se := range stored {
		ev, derr := a.gateDecider.UnmarshalEvent(se.Type, se.Data)
		if derr != nil {
			continue
		}

		prevGate := tempCS.Gate
		tempCS = a.gateDecider.Evolve(tempCS, ev)

		// If the gate type or target states changed, record a reason
		gateChanged := prevGate == nil || reflect.TypeOf(prevGate) != reflect.TypeOf(tempCS.Gate)
		if !gateChanged && prevGate != nil {
			// Deep state comparison
			gateChanged = !reflect.DeepEqual(prevGate, tempCS.Gate)
		}

		if gateChanged {
			desc := fmt.Sprintf("Event %s occurred", se.Type)
			reasons = append(reasons, api.InspectGateReason{
				EventType:   se.Type,
				OccurredAt:  se.OccurredAt,
				Description: desc,
			})
		}
	}

	var targets []api.InspectGateTarget
	switch v := cs.Gate.(type) {
	case reconcile.Pending:
		for _, t := range v.Targets {
			targets = append(targets, api.InspectGateTarget{Class: t.Class, Target: t.Target, GrantName: t.GrantName, State: string(t.Grant), Requester: v.Lease.Requester})
		}
	case reconcile.Satisfied:
		for _, t := range v.Targets {
			targets = append(targets, api.InspectGateTarget{Class: t.Class, Target: t.Target, GrantName: t.GrantName, State: string(t.Grant), Requester: v.Lease.Requester})
		}
	case reconcile.Blocked:
		for _, t := range v.Targets {
			targets = append(targets, api.InspectGateTarget{Class: t.Class, Target: t.Target, GrantName: t.GrantName, State: string(t.Grant), Requester: v.Lease.Requester})
		}
	}

	gateStateStr := "NotClassified"
	if cs.Gate != nil {
		gateStateStr = strings.TrimPrefix(fmt.Sprintf("%T", cs.Gate), "reconcile.")
	}

	out := api.InspectGateDetail{
		Pr:          pr,
		Environment: env,
		GateState:   gateStateStr,
		Targets:     targets,
		Reasons:     reasons,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) handleInspectClaims(w http.ResponseWriter, r *http.Request, env string) {
	state, _, err := a.claimsDecider.Load(a.eventStore, envStreamID(env))
	if err != nil {
		http.Error(w, "load claims", http.StatusInternalServerError)
		return
	}

	var claimsList []api.InspectClaim
	for stack, cl := range state {
		claimsList = append(claimsList, api.InspectClaim{
			Stack:     stack,
			Pr:        cl.PR,
			ExpiresAt: cl.ExpiresAt,
		})
	}

	out := api.InspectClaimsSet{
		Environment: env,
		Claims:      claimsList,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) handleInspectEvents(w http.ResponseWriter, r *http.Request, stream string, params api.InspectEventsParams) {
	after := 0
	if params.After != nil {
		after = *params.After
	}

	rows, err := a.db.Query(
		`SELECT version, type, occurred_at, data FROM events WHERE stream_id = ? AND version > ? ORDER BY version`,
		stream, after)
	if err != nil {
		http.Error(w, "query events", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var eventsList []api.InspectEvent
	for rows.Next() {
		var ev api.InspectEvent
		if err := rows.Scan(&ev.Version, &ev.Type, &ev.OccurredAt, &ev.Data); err != nil {
			http.Error(w, "scan event", http.StatusInternalServerError)
			return
		}
		eventsList = append(eventsList, ev)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(eventsList)
}

func (a *App) handleInspectOverview(w http.ResponseWriter, r *http.Request) {
	// Find all unique PR numbers (capped at 100 most recent for performance)
	rows, err := a.db.Query(`
		SELECT DISTINCT pr FROM (
			SELECT pr FROM executions
			UNION
			SELECT pr FROM gate_targets
			UNION
			SELECT pr FROM pr_meta
		) ORDER BY pr DESC LIMIT 100`)
	if err != nil {
		http.Error(w, "query prs", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var prs []int
	for rows.Next() {
		var pr int
		if err := rows.Scan(&pr); err == nil {
			prs = append(prs, pr)
		}
	}

	// Pre-load claims state for the current environment outside the loop (Fixes N+1 loads)
	env := a.cfg.Environment
	var claimsState claims.ClaimSet
	if env != "" {
		if state, _, cerr := a.claimsDecider.Load(a.eventStore, envStreamID(env)); cerr == nil {
			claimsState = state
		}
	}

	overview := []api.InspectPRSummary{}
	for _, pr := range prs {
		execs, err := store.ListExecutionsForPR(a.db, pr)
		if err != nil {
			continue
		}
		meta, metaOK, err := store.GetPRMetaByPR(a.db, pr)
		if err != nil {
			continue
		}

		repo := ""
		if len(execs) > 0 {
			repo = execs[0].Repo
		} else if metaOK {
			repo = meta.Repo
		}

		// Read grants
		grows, err := a.db.Query(`
			SELECT pr, environment, class, target, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,'')
			FROM gate_targets WHERE pr = ?`, pr)
		var openGrants []api.InspectGrant
		if err == nil {
			for grows.Next() {
				var g api.InspectGrant
				if err := grows.Scan(&g.Pr, &g.Environment, &g.Class, &g.Target, &g.GrantName, &g.State, &g.Requester); err == nil {
					openGrants = append(openGrants, g)
				}
			}
			grows.Close()
		}

		// Read claims from pre-loaded state
		var claimsList []api.InspectClaim
		if env != "" && claimsState != nil {
			for stack, cl := range claimsState {
				if cl.PR == pr {
					claimsList = append(claimsList, api.InspectClaim{
						Stack:     stack,
						Pr:        cl.PR,
						ExpiresAt: cl.ExpiresAt,
					})
				}
			}
		}

		// Load GateState name
		gateStateStr := "NotClassified"
		if env != "" {
			cs, _, err := a.gateDecider.Load(a.eventStore, execStreamID(pr, env))
			if err == nil && cs.Gate != nil {
				gateStateStr = strings.TrimPrefix(fmt.Sprintf("%T", cs.Gate), "reconcile.")
			}
		}

		summaries := []api.ExecutionSummary{}
		for _, e := range execs {
			progressLabel := ""
			if e.ProgressLabel.Valid {
				progressLabel = e.ProgressLabel.String
			} else {
				progressLabel = phaseLabel(events.Phase(e.Phase), 0, 0, 0)
			}
			progressPct := 0
			if e.ProgressPct.Valid {
				progressPct = int(e.ProgressPct.Int64)
			} else {
				var weights []config.PhaseWeight
				if a.cfg.Progress != nil {
					if isApplyContext(e.StatusContext) {
						weights = a.cfg.Progress.Apply
					} else {
						weights = a.cfg.Progress.Plan
					}
				}
				_, _, progressPct = progress(weights, events.Phase(e.Phase), 0, 0, 0)
			}

			summaries = append(summaries, api.ExecutionSummary{
				Id:            e.ID,
				Repo:          e.Repo,
				Sha:           e.SHA,
				Pr:            e.PR,
				Environment:   e.Environment,
				Context:       e.StatusContext,
				Status:        e.Status,
				Phase:         e.Phase,
				ProgressLabel: progressLabel,
				ProgressPct:   progressPct,
				CreatedAt:     e.CreatedAt,
				SupersededBy:  e.SupersededBy,
				LogUrl:        e.LogURL,
			})
		}

		sum := api.InspectPRSummary{
			Pr:         pr,
			Repo:       repo,
			GateState:  gateStateStr,
			OpenGrants: openGrants,
			Claims:     claimsList,
			Executions: summaries,
		}
		if metaOK {
			sum.Meta = &api.PRMeta{
				Title:       meta.Title,
				Body:        meta.Body,
				AuthorLogin: meta.AuthorLogin,
				HeadRef:     meta.HeadRef,
				Url:         meta.URL,
				AutoMerge:   meta.AutoMerge,
			}
		}
		overview = append(overview, sum)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(overview)
}

func (a *App) handleInspectPool(w http.ResponseWriter, r *http.Request) {
	env := a.cfg.Environment

	// Build default empty slots map from config
	slots := []api.InspectPoolSlot{}
	pool := a.cfg.RequesterPool
	for _, sa := range pool {
		slots = append(slots, api.InspectPoolSlot{
			Requester: sa,
			Occupied:  false,
		})
	}

	// Query gate_targets table for any active occupants in this environment
	rows, err := a.db.Query(`
		SELECT pr, environment, COALESCE(grant_name,''), COALESCE(state,''), COALESCE(requester,''),
		       strftime('%s', 'now') - strftime('%s', updated_at)
		FROM gate_targets WHERE state IN ('AWAITING', 'ACTIVATING', 'ACTIVE') AND environment = ?`, env)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pr, elapsed int
			var envStr, name, state, req string
			if err := rows.Scan(&pr, &envStr, &name, &state, &req, &elapsed); err == nil && req != "" {
				// Match against configured pool slots
				for i := range slots {
					if slots[i].Requester == req {
						slots[i].Occupied = true
						slots[i].Pr = ptr(pr)
						slots[i].Environment = ptr(envStr)
						slots[i].GrantName = ptr(name)
						slots[i].State = ptr(state)
						slots[i].ElapsedSeconds = ptr(elapsed)
					}
				}
			}
		}
	}

	// Find waiting PRs currently blocked by slot collisions
	waitingList := []api.InspectPoolWaitingPR{}

	// Retrieve all unique active/open PR numbers
	prows, err := a.db.Query(`
		SELECT DISTINCT pr FROM (
			SELECT pr FROM executions
			UNION
			SELECT pr FROM gate_targets
			UNION
			SELECT pr FROM pr_meta
		) ORDER BY pr DESC`)
	if err == nil {
		defer prows.Close()
		var prs []int
		for prows.Next() {
			var pr int
			if err := prows.Scan(&pr); err == nil {
				prs = append(prs, pr)
			}
		}

		for _, pr := range prs {
			streamID := execStreamID(pr, env)
			cs, _, err := a.gateDecider.Load(a.eventStore, streamID)
			if err == nil && cs.Gate != nil {
				if blocked, ok := cs.Gate.(reconcile.Blocked); ok {
					reason := string(blocked.By.Reason)
					if reason == "slot_foreign_open" || reason == "slot_self" {
						waitingList = append(waitingList, api.InspectPoolWaitingPR{
							Pr:          pr,
							Environment: env,
							Reason:      reason,
							BlockerPr:   blocked.By.ByPR,
							BlockerEnv:  blocked.By.ByEnv,
						})
					}
				}
			}
		}
	}

	out := api.InspectPoolSet{
		Environment: env,
		Slots:       slots,
		Waiting:     waitingList,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
