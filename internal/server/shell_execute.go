package server

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// execute performs the side-effecting Actions and returns the ObservedGrant
// results that feed the next Step (only RequestGrant yields). RenderCheckRun /
// PostCommitStatus / PublishSSE are output-only. Slot collisions are resolved
// against GitHub here (self / closed) and surfaced as ObservedGrant.Collision.
func (sh *Shell) execute(ctx context.Context, cs reconcile.ChangeSet, repo string, actions []reconcile.Action) []reconcile.ObservedGrant {
	var results []reconcile.ObservedGrant
	for _, a := range actions {
		switch act := a.(type) {
		case reconcile.RequestGrant:
			if sh.app.Approval == nil {
				continue
			}
			g, err := sh.app.Approval.RequestGrant(ctx, approval.Request{
				Class: act.Class, Target: act.Target, PR: cs.PR, Environment: cs.Environment, Requester: act.Requester,
			})
			if err != nil {
				results = append(results, sh.observeError(ctx, cs, repo, act, err))
				continue
			}
			results = append(results, reconcile.ObservedGrant{
				Class: act.Class, Target: act.Target, Name: g.Name, State: g.State, Requester: g.Requester,
			})
		case reconcile.RevokeGrant:
			if sh.app.Approval == nil {
				continue
			}
			if err := sh.app.Approval.Revoke(ctx, approval.Request{
				Class: act.Class, Target: act.Target, PR: act.PR, Environment: act.Environment,
			}); err != nil {
				log.Printf("shell: revoke pr=%d env=%s %s/%s: %v", act.PR, act.Environment, act.Class, act.Target, err)
			}
		case reconcile.RenderCheckRun:
			sh.renderCheckRun(ctx, cs, act)
		case reconcile.PostCommitStatus:
			// drive() already posts commit status on terminal renders; no separate path needed.
		case reconcile.PublishSSE:
			sh.publishSSE(cs)
		}
	}
	return results
}

// observeError maps a RequestGrant error into an ObservedGrant: a slot collision
// is resolved against GitHub (BySelf / ByPRAbandoned) so the pure core can decide;
// any other error leaves State "" (target stays Pending, retried next tick).
func (sh *Shell) observeError(ctx context.Context, cs reconcile.ChangeSet, repo string, act reconcile.RequestGrant, err error) reconcile.ObservedGrant {
	var col *approval.SlotCollisionError
	if !errors.As(err, &col) {
		log.Printf("shell: request grant pr=%d env=%s %s/%s: %v", cs.PR, cs.Environment, act.Class, act.Target, err)
		return reconcile.ObservedGrant{Class: act.Class, Target: act.Target, State: ""}
	}
	b := col.BlockingGrant.Request
	bySelf := b.PR == cs.PR
	abandoned := false
	if !bySelf {
		if c, cerr := sh.app.gh.PRAbandoned(ctx, repo, b.PR); cerr == nil {
			abandoned = c
		} else {
			log.Printf("shell: slot-collision PRAbandoned(%d): %v", b.PR, cerr)
		}
	}
	return reconcile.ObservedGrant{
		Class: act.Class, Target: act.Target,
		Collision: &reconcile.Collision{ByPR: b.PR, ByEnv: b.Environment, BySelf: bySelf, ByPRAbandoned: abandoned},
	}
}

// renderCheckRun re-renders the check run (which also publishes SSE) for the
// changeset's latest execution. No-op when there is no execution yet.
func (sh *Shell) renderCheckRun(ctx context.Context, cs reconcile.ChangeSet, act reconcile.RenderCheckRun) {
	if id, ok := store.LatestExecutionID(sh.app.db, cs.PR, cs.Environment); ok {
		sh.app.drive(ctx, id, strings.TrimRight(sh.app.cfg.PublicBaseURL, "/"), act.Terminal)
	}
}

// publishSSE notifies the live-page hub that the changeset's execution changed.
// Used by transitions that emit no RenderCheckRun (e.g. PRClosed).
func (sh *Shell) publishSSE(cs reconcile.ChangeSet) {
	if sh.app.hub == nil {
		return
	}
	if id, ok := store.LatestExecutionID(sh.app.db, cs.PR, cs.Environment); ok {
		sh.app.hub.publish("exec:"+id, "changed")
	}
}
