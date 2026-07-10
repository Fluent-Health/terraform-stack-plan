package reconcile

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// Signal is the union of everything that can drive a ChangeSet.
type Signal interface{ isSignal() }

// --- edge-triggered told-facts from the CI runner ---

type RunnerInit struct{ Exec Execution }
type RunnerPhase struct{ Phase events.Phase }
type RunnerUpdate struct {
	Stack  string
	Status events.Status
	Detail string
}
type RunnerFinalize struct {
	Failed         bool
	ReportMarkdown string
	Projects       map[string]string            // stack path → grouping/target key
	Categories     map[string][]events.Category // stack path → matched categories
	Moving         []string                     // stack paths adopting via cross-state move
	Gates          []events.GateTarget          // (class,target) pairs needing approval
	// ApplyContext marks a finalize from the post-merge apply (apply/<env>) rather
	// than the plan gate. An apply finalize is a RECOVERY signal, not an authority:
	// it may add/refresh gate targets but must never weaken a gate the plan already
	// established (issue #103) — so decideFinalize unions the prior targets and never
	// prunes when this is set. A plan finalize (false) stays authoritative.
	ApplyContext bool
}

// --- observed external world ---

type PRClosed struct{}

// GrantsObserved carries grant observations for THIS changeset: either the
// results of RequestGrant actions (fixpoint feedback) or a re-list. Grants not
// mentioned are left unchanged.
type GrantsObserved struct{ Grants []ObservedGrant }

// GateTick is the periodic backstop re-observation; Grants is a full re-list of
// the changeset's targets. Processed identically to GrantsObserved.
type GateTick struct{ Grants []ObservedGrant }

// --- serve-initiated run triggering (webhook → executor) ---

// RunRequested asks for a CI run: pull_request opened/synchronize → plan for
// the head SHA; push to main → apply for the merge commit; check_run
// rerequested → the same kind again with Rerun set (forces a new attempt even
// for an already-seen SHA).
type RunRequested struct {
	Kind   string // RunKindPlan | RunKindApply
	SHA    string
	Branch string
	Rerun  bool
}

// RunStartResult is the fixpoint feedback from a StartRun action (like
// GrantsObserved for RequestGrant): the executor either accepted the build
// (BuildRef) or failed (Err).
type RunStartResult struct {
	Kind        string
	ExecutionID string
	BuildRef    string
	Err         string
}

// InboundBuild is an observed Cloud Build lifecycle event for a build serve may
// NOT have launched — a native-check Re-run or a console rebuild. The shell has
// already correlated it to this ChangeSet's (pr, env) and derived the run Kind
// (from the trigger) + the resolved commit SHA. Decide reconciles it onto the
// PR's stuck run; the fail-safe backstop for the adopted build stays the watchdog.
type InboundBuild struct {
	Kind     string // RunKindPlan | RunKindApply
	SHA      string // resolved commit sha of the build
	BuildRef string // Cloud Build build id
}

// --- post-apply ---

type ApplySucceeded struct{}

func (RunnerInit) isSignal()     {}
func (RunnerPhase) isSignal()    {}
func (RunnerUpdate) isSignal()   {}
func (RunnerFinalize) isSignal() {}
func (PRClosed) isSignal()       {}
func (GrantsObserved) isSignal() {}
func (GateTick) isSignal()       {}
func (ApplySucceeded) isSignal() {}
func (RunRequested) isSignal()   {}
func (RunStartResult) isSignal() {}
func (InboundBuild) isSignal()   {}

// ObservedGrant is one grant fact the shell gathered for a (class,target).
type ObservedGrant struct {
	Class     string
	Target    string
	Name      string              // backend-assigned id ("" if none)
	State     approval.GrantState // "" == no grant present for this changeset
	Requester string              // leased identity ("" if N/A)
	Collision *Collision          // set when a RequestGrant hit a SlotCollisionError
}

// Collision describes a slot blocker the shell already resolved against GitHub.
type Collision struct {
	ByPR          int
	ByEnv         string
	BySelf        bool // ByPR == this changeset's PR (different env)
	ByPRAbandoned bool // shell looked up the blocker PR's GitHub state (closed && !merged)
}
