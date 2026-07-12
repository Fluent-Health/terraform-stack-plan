package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
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
		out = append(out, api.ExecutionSummary{
			Id:           e.ID,
			Repo:         e.Repo,
			Sha:          e.SHA,
			Pr:           e.PR,
			Environment:  e.Environment,
			Context:      e.StatusContext,
			Status:       e.Status,
			Phase:        e.Phase,
			CreatedAt:    e.CreatedAt,
			SupersededBy: e.SupersededBy,
			LogUrl:       e.LogURL,
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
							// Trigger self-healing nudge
							a.reconcileBackground(g.Pr, g.Environment)
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
