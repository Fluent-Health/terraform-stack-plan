// Package reconcile is the pure functional core for the server's gate/grant
// and execution-lifecycle state. It performs no I/O: Step takes an observed
// World plus a Signal and returns the new ChangeSet plus the minimal set of
// Actions the imperative shell must execute. See
// docs/superpowers/specs/2026-06-15-reconciler-core-design.md.
package reconcile

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

// ChangeSet is the reconciliation unit: one PR's work in one environment.
type ChangeSet struct {
	PR            int
	Environment   string
	Gate          GateState
	CheckOverride *CheckOverride
	// Runs tracks serve-initiated CI runs, keyed by kind (plan/apply). Only
	// the run-start lifecycle lives here (webhook → build started); once the
	// runner reports, the internal/execution aggregate (stream "run:<execID>")
	// carries the execution facts.
	Runs map[string]Run
}

type CheckOverride struct {
	CheckName  string `json:"check_name"`
	Conclusion string `json:"conclusion"`
	Actor      string `json:"actor"`
	Reason     string `json:"reason"`
}

// Run kinds — the two CI run flavors serve can trigger.
const (
	RunKindPlan  = "plan"
	RunKindApply = "apply"
)

// Run is the serve-initiated lifecycle of one CI run: requested by a webhook,
// queued (execution + check run exist), started (build accepted by the
// executor backend), or terminally start_failed / superseded.
type Run struct {
	ExecutionID string
	Kind        string // RunKindPlan | RunKindApply
	SHA         string
	Branch      string
	Attempt     int      // bumps on rerun / retry-after-failure (part of the deterministic execution id)
	BuildRef    string   // executor backend reference ("" until started)
	Phase       RunPhase //
}

// RunPhase is the run-start lifecycle phase.
type RunPhase string

const (
	RunPhaseQueued      RunPhase = "queued"       // execution created, StartRun pending/issued
	RunPhaseStarted     RunPhase = "started"      // executor accepted the build
	RunPhaseStartFailed RunPhase = "start_failed" // executor refused / start errored
	RunPhaseSuperseded  RunPhase = "superseded"   // a newer SHA replaced this run
	RunPhaseCompleted   RunPhase = "completed"    // the runner took over and finalized
)

// Live reports whether the run is still in flight (may be superseded/cancelled).
func (r Run) Live() bool {
	return r.Phase == RunPhaseQueued || r.Phase == RunPhaseStarted
}

// Lease is the pooled requester identity leased once per ChangeSet. Empty
// Requester means undecided.
type Lease struct{ Requester string }

// Target is one (class,target) gate and its observed grant.
type Target struct {
	Class     string
	Target    string
	GrantName string              // backend-assigned id; "" until created
	Grant     approval.GrantState // observed grant lifecycle; "" == no grant/absent
}

// GateState is a sum type: a gate is EXACTLY one of these.
type GateState interface{ isGate() }

type NotClassified struct{} // never planned → apply fails closed
type Clean struct{}         // classified, zero gates → apply passes
type Pending struct {
	Targets []Target
	Lease   Lease
}
type Satisfied struct {
	Targets []Target
	Lease   Lease
}
type Blocked struct {
	Targets []Target
	Lease   Lease
	By      Blocker
}

func (NotClassified) isGate() {}
func (Clean) isGate()         {}
func (Pending) isGate()       {}
func (Satisfied) isGate()     {}
func (Blocked) isGate()       {}

// Blocker explains why a gate is Blocked.
type Blocker struct {
	Reason BlockReason
	ByPR   int    // slot blocker's PR (0 if N/A)
	ByEnv  string // slot blocker's environment
}

type BlockReason string

const (
	ReasonDenied       BlockReason = "denied"
	ReasonExpired      BlockReason = "expired"
	ReasonRevoked      BlockReason = "revoked"
	ReasonSlotForeign  BlockReason = "slot_foreign_open" // another open PR holds the slot (⑥)
	ReasonSlotSelf     BlockReason = "slot_self"         // same PR, another env, holds the slot (⑥)
	ReasonBackendError BlockReason = "backend_error"
)
