package reconcile

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// Event is a past-tense domain fact produced by Decide and folded by Evolve.
// Events are domain facts ONLY — presentation (check run / SSE) is never an
// Event; React projects that from the event stream. See the Phase 3 design.
type Event interface{ isEvent() }

// --- execution facts ---

type ExecutionStarted struct{ Exec Execution }
type PhaseChanged struct{ Phase events.Phase }
type StackStatusChanged struct {
	Stack  string
	Status events.Status
	Detail string
}
type ExecutionFailed struct{}
type StacksClassified struct {
	Projects   map[string]string
	Categories map[string][]events.Category
}

// --- gate-lifecycle facts ---

// Classified establishes/replaces the gate target set. Evolve carries forward
// still-live prior grants (gap ② anti-clobber); Decide has already resolved the
// effective set (plan-authoritative vs apply-recovery union).
type Classified struct{ Gates []events.GateTarget }
type GrantObserved struct {
	Class, Target, Name string
	State               approval.GrantState
	Requester           string
}
type GrantCleared struct{ Class, Target string }
type GateTargetRequested struct{ Class, Target, Requester string } // Requester pins the lease (step.go:140)
type GateSatisfied struct{}
type GateBlocked struct {
	Reason BlockReason
	ByPR   int
	ByEnv  string
}
type TargetRevoked struct {
	Class, Target string
	PR            int
	Env           string
}

// GatePassed and GateReleased both fold the gate to Clean. They are distinct
// facts so React can present them differently: GatePassed (plan-clean / nothing
// to gate) renders success; GateReleased (post-apply cleanup) renders nothing.
type GatePassed struct{}
type GateReleased struct{}

// --- run-triggering facts (serve-initiated CI runs) ---

// RunQueued records that a run request was accepted: the shell creates the
// execution row + a "queued" check run and issues StartRun. ExecutionID is
// minted deterministically by Decide (pr/env/kind/sha/attempt — the pure core
// cannot use randomness).
type RunQueued struct {
	Kind        string
	SHA         string
	Branch      string
	ExecutionID string
	Attempt     int
}

// RunStarted records the executor accepting the build.
type RunStarted struct {
	Kind        string
	ExecutionID string
	BuildRef    string
}

// RunStartFailed records the executor refusing / erroring — surfaced as a
// terminal check failure (vs the pre-driver era's silent nothing).
type RunStartFailed struct {
	Kind        string
	ExecutionID string
	Reason      string
}

// RunSuperseded records a newer SHA replacing an in-flight run (plan only —
// applies are never superseded). The shell cancels the old build (when a
// BuildRef exists) and marks the old execution superseded by the new one.
type RunSuperseded struct {
	Kind           string
	OldExecutionID string
	OldBuildRef    string
	NewExecutionID string
	NewSHA         string
}

// --- claim-ledger fact ---

type ClaimReleased struct {
	PR          int
	Environment string
}

// --- trigger fact (presentation disambiguation) ---

// PRClosedRecorded marks a PR-closure teardown so React renders SSE-only (no
// check run), distinct from an externally-observed revocation. Named *Recorded
// to avoid colliding with the PRClosed Signal.
type PRClosedRecorded struct{}

func (ExecutionStarted) isEvent()    {}
func (PhaseChanged) isEvent()        {}
func (StackStatusChanged) isEvent()  {}
func (ExecutionFailed) isEvent()     {}
func (StacksClassified) isEvent()    {}
func (Classified) isEvent()          {}
func (GrantObserved) isEvent()       {}
func (GrantCleared) isEvent()        {}
func (GateTargetRequested) isEvent() {}
func (GateSatisfied) isEvent()       {}
func (GateBlocked) isEvent()         {}
func (TargetRevoked) isEvent()       {}
func (GatePassed) isEvent()          {}
func (GateReleased) isEvent()        {}
func (ClaimReleased) isEvent()       {}
func (PRClosedRecorded) isEvent()    {}
func (RunQueued) isEvent()           {}
func (RunStarted) isEvent()          {}
func (RunStartFailed) isEvent()      {}
func (RunSuperseded) isEvent()       {}
