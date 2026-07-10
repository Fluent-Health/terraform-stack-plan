package reconcile

// decideInboundBuild reconciles a Cloud Build build serve did not launch onto the
// PR's stuck run. It fires ONLY for a genuinely new build (different id) matching
// the current run's commit when serve has already given up on that run
// (start_failed / completed / superseded) — a live run is left alone, and a build
// serve already tracks is a no-op (the watchdog + runner own its lifecycle). The
// transition supersedes the stuck run's execution and adopts the new build under a
// fresh deterministic run execution id (attempt bumped), so the stale FAILED check
// is shadowed by a new in-progress one and the watchdog re-points to the new build.
func decideInboundBuild(state ChangeSet, sig InboundBuild) []Event {
	if sig.Kind != RunKindPlan && sig.Kind != RunKindApply {
		return nil
	}
	if sig.BuildRef == "" || sig.SHA == "" {
		return nil
	}
	r, ok := state.Runs[sig.Kind]
	if !ok {
		return nil // no serve-initiated run to reconcile onto
	}
	if r.BuildRef == sig.BuildRef {
		return nil // already tracking this exact build (redelivery / our own launch)
	}
	if r.SHA != sig.SHA {
		return nil // build is for a different commit than the current run
	}
	if r.Live() {
		return nil // still tracking a live build of our own; don't disturb it
	}
	attempt := r.Attempt + 1
	newID := runExecutionID(state.PR, state.Environment, sig.Kind, sig.SHA, attempt)
	return []Event{
		RunSuperseded{
			Kind: sig.Kind, OldExecutionID: r.ExecutionID, OldBuildRef: r.BuildRef,
			NewExecutionID: newID, NewSHA: sig.SHA,
		},
		RunAdopted{
			Kind: sig.Kind, ExecutionID: newID, SHA: sig.SHA, Branch: r.Branch,
			Attempt: attempt, BuildRef: sig.BuildRef,
		},
	}
}
