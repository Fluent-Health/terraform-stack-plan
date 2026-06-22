package reconcile

// Action is an effect the pure core wants performed but cannot do itself.
type Action interface{ isAction() }

// RequestGrant asks the backend to ensure an open grant. It YIELDS a result
// (grant name + leased requester, or a Collision) the shell feeds back as a
// GrantsObserved signal. Requester pins the lease; "" lets the backend lease.
type RequestGrant struct {
	Class     string
	Target    string
	Requester string
}

// RevokeGrant revokes the open grant for (class,target,PR,environment).
// Idempotent; produces no result. PR/Environment default to the changeset's
// when zero/empty but are set explicitly when revoking a foreign slot blocker.
type RevokeGrant struct {
	Class       string
	Target      string
	PR          int
	Environment string
}

// RenderCheckRun re-renders the GitHub check run from current state. Pure
// output; no result.
type RenderCheckRun struct {
	Terminal   bool
	Conclusion string // "success" | "action_required" | "failure" | "" (in-progress)
}

// PostCommitStatus posts the commit status. Pure output; no result.
type PostCommitStatus struct {
	State       string // "success" | "failure" | "pending"
	Description string
}

// PublishSSE notifies the live-page hub that state changed. Pure output.
type PublishSSE struct{}

// ReleaseClaim releases the merge-lock stack claim a PR holds in an environment.
// Emitted post-apply (the apply is done, so the claim it held is no longer
// needed). The shell deletes the claims and re-evaluates the env's held
// apply-lock checks (cross-PR I/O). Idempotent.
type ReleaseClaim struct {
	PR          int
	Environment string
}

func (RequestGrant) isAction()     {}
func (RevokeGrant) isAction()      {}
func (RenderCheckRun) isAction()   {}
func (PostCommitStatus) isAction() {}
func (PublishSSE) isAction()       {}
func (ReleaseClaim) isAction()     {}

// YieldsResult reports whether an action produces a GrantsObserved on the
// fixpoint loop (only RequestGrant does). The shell uses this to decide when
// the fixpoint has converged.
func YieldsResult(a Action) bool {
	_, ok := a.(RequestGrant)
	return ok
}
