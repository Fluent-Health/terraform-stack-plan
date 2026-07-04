package reconcile

import (
	"fmt"
	"strings"
)

// decideRunRequested handles a webhook-driven CI run request.
//
// Idempotency: a request for the same (kind, sha) while that run is live and
// no explicit rerun was asked is a webhook redelivery → no events. A new SHA
// while a plan run is live supersedes it (a stale plan is worthless); apply
// runs are never superseded — an in-flight apply must finish. A rerun (the
// check's Re-run button) or a retry after start_failed re-queues the same SHA
// with a bumped attempt.
func decideRunRequested(state ChangeSet, sig RunRequested) []Event {
	if sig.Kind != RunKindPlan && sig.Kind != RunKindApply {
		return nil
	}
	if sig.SHA == "" {
		return nil
	}
	prior, exists := state.Runs[sig.Kind]
	attempt := 1
	var evs []Event
	if exists {
		// Same (kind, sha) without an explicit rerun is a webhook redelivery —
		// no-op regardless of the run's phase: a late redelivery must never
		// re-trigger a build (an apply in particular). Only the Re-run button
		// (Rerun) forces a fresh attempt.
		if prior.SHA == sig.SHA && !sig.Rerun {
			return nil
		}
		// Attempts are monotonic across ALL requests, not per SHA: a force-push
		// round-trip (A → B → A) must not re-mint A's first execution id — that
		// row is superseded/dead in the store.
		attempt = prior.Attempt + 1
	}
	execID := runExecutionID(state.PR, state.Environment, sig.Kind, sig.SHA, attempt)
	if exists && prior.Live() && sig.Kind == RunKindPlan {
		evs = append(evs, RunSuperseded{
			Kind:           sig.Kind,
			OldExecutionID: prior.ExecutionID,
			OldBuildRef:    prior.BuildRef,
			NewExecutionID: execID,
			NewSHA:         sig.SHA,
		})
	}
	if exists && prior.Live() && sig.Kind == RunKindApply {
		// Never disturb a live apply; the request is dropped (a re-dispatch
		// after it finishes will queue normally).
		return nil
	}
	return append(evs, RunQueued{
		Kind: sig.Kind, SHA: sig.SHA, Branch: sig.Branch,
		ExecutionID: execID, Attempt: attempt,
	})
}

// runCompletionEvents closes the run-start lifecycle when the runner
// finalizes: the matching kind's run (live, or "start-failed" — a client-side
// start timeout whose build evidently ran) completes. The finalize itself is
// the proof the build executed; without this, a finished apply would read as
// live forever and every later apply request would be dropped.
func runCompletionEvents(state ChangeSet, applyContext bool) []Event {
	kind := RunKindPlan
	if applyContext {
		kind = RunKindApply
	}
	r, ok := state.Runs[kind]
	if !ok || (!r.Live() && r.Phase != RunPhaseStartFailed) {
		return nil
	}
	return []Event{RunCompleted{Kind: kind, ExecutionID: r.ExecutionID}}
}

// decideRunStartResult folds the executor's answer to a StartRun action.
// Stale feedback (a different execution id than the current run's) is dropped
// — a supersede may have raced the start.
func decideRunStartResult(state ChangeSet, sig RunStartResult) []Event {
	r, ok := state.Runs[sig.Kind]
	if !ok || r.ExecutionID != sig.ExecutionID {
		return nil
	}
	if sig.Err != "" {
		if r.Phase == RunPhaseStartFailed {
			return nil
		}
		return []Event{RunStartFailed{Kind: sig.Kind, ExecutionID: sig.ExecutionID, Reason: sig.Err}}
	}
	if r.Phase == RunPhaseStarted && r.BuildRef == sig.BuildRef {
		return nil
	}
	return []Event{RunStarted{Kind: sig.Kind, ExecutionID: sig.ExecutionID, BuildRef: sig.BuildRef}}
}

// RunExecutionIDPrefix marks serve-minted run execution ids (see
// runExecutionID). The store's stuck-run query and the queued check render key
// off it to distinguish serve-initiated runs from runner-created executions.
const RunExecutionIDPrefix = "run-"

// IsRunExecutionID reports whether id was minted by runExecutionID.
func IsRunExecutionID(id string) bool {
	return strings.HasPrefix(id, RunExecutionIDPrefix)
}

// runExecutionID mints the deterministic execution id for a run. Decide is
// pure, so the id must be a function of the request: pr/env/kind/sha/attempt.
// The attempt counter keeps rerun ids unique.
func runExecutionID(pr int, env, kind, sha string, attempt int) string {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("run-%d-%s-%s-%s-a%d", pr, env, kind, short, attempt)
}
