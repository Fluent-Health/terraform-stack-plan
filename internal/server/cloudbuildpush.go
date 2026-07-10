package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// cloudBuild is the subset of the Cloud Build resource that the project's
// `cloud-builds` Pub/Sub topic publishes that we correlate on.
type cloudBuild struct {
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	Substitutions    map[string]string `json:"substitutions"`
	SourceProvenance struct {
		ResolvedRepoSource struct {
			CommitSha string `json:"commitSha"`
		} `json:"resolvedRepoSource"`
	} `json:"sourceProvenance"`
}

// handleCloudBuildPush ingests a Cloud Build lifecycle event (from the project's
// `cloud-builds` topic, OIDC-verified) for a build serve may NOT have launched — a
// native-check Re-run or a console rebuild. It correlates the build to a
// serve-initiated run by metadata and feeds an InboundBuild signal so the stuck
// terraform/<env> check is reconciled onto the new build. 404 unless run
// triggering is armed, a push verifier is set, and trigger names are configured.
func (a *App) handleCloudBuildPush(w http.ResponseWriter, r *http.Request) {
	if a.PushVerifier == nil || !a.runTriggerArmed() || len(a.cfg.BuildTriggerNames) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" || tok == r.Header.Get("Authorization") {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	email, err := a.PushVerifier(r.Context(), tok)
	if err != nil {
		http.Error(w, "invalid OIDC token", http.StatusUnauthorized)
		return
	}
	if a.cfg.PushServiceAccount != "" && email != a.cfg.PushServiceAccount {
		http.Error(w, "unauthorized push identity", http.StatusForbidden)
		return
	}
	var env pushEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		http.Error(w, "bad push envelope", http.StatusBadRequest)
		return
	}
	raw, derr := base64.StdEncoding.DecodeString(env.Message.Data)
	if derr == nil {
		var b cloudBuild
		if json.Unmarshal(raw, &b) == nil && b.ID != "" {
			a.reconcileInboundBuild(r.Context(), b)
		}
	}
	w.WriteHeader(http.StatusNoContent) // always ack — a wedged subscription is worse than a dropped event
}

// reconcileInboundBuild correlates a Cloud Build to a serve-initiated run and
// dispatches an InboundBuild signal. Silent no-op when the build isn't from one of
// serve's triggers or can't be attributed to a PR.
func (a *App) reconcileInboundBuild(ctx context.Context, b cloudBuild) {
	kind := a.cfg.BuildTriggerNames[b.Substitutions["TRIGGER_NAME"]]
	if kind == "" {
		return // not one of serve's plan/apply triggers
	}
	sha := b.Substitutions["COMMIT_SHA"]
	if sha == "" {
		sha = b.SourceProvenance.ResolvedRepoSource.CommitSha
	}
	if sha == "" {
		return
	}
	ctxName := runContext(kind, a.cfg.Environment)

	// Recover the owning execution: _EXECUTION_ID → _PR_NUMBER → (env, ctx, sha).
	var owner store.Execution
	if id := b.Substitutions["_EXECUTION_ID"]; id != "" {
		if e, err := store.GetExecution(a.db, id); err == nil {
			owner = e
		}
	}
	if owner.PR == 0 {
		if n, _ := strconv.Atoi(b.Substitutions["_PR_NUMBER"]); n > 0 {
			if id, ok := store.LatestExecutionID(a.db, n, a.cfg.Environment); ok {
				if e, err := store.GetExecution(a.db, id); err == nil {
					owner = e
				}
			}
		}
	}
	if owner.PR == 0 {
		if id, ok, err := store.FindExecutionBySHA(a.db, a.cfg.Environment, ctxName, sha); err == nil && ok {
			if e, gerr := store.GetExecution(a.db, id); gerr == nil {
				owner = e
			}
		}
	}
	if owner.PR == 0 {
		return // can't attribute this build to a PR
	}
	if err := a.shell.Handle(ctx, owner.PR, a.cfg.Environment, owner.Repo, reconcile.InboundBuild{
		Kind: kind, SHA: sha, BuildRef: b.ID,
	}); err != nil {
		log.Printf("cloud-build push: reconcile pr=%d build=%s: %v", owner.PR, b.ID, err)
	}
}
