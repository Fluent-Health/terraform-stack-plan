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
