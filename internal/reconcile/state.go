// Package reconcile is the pure functional core for the server's gate/grant
// and execution-lifecycle state. It performs no I/O: Step takes an observed
// World plus a Signal and returns the new ChangeSet plus the minimal set of
// Actions the imperative shell must execute. See
// docs/superpowers/specs/2026-06-15-reconciler-core-design.md.
package reconcile

import (
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// ChangeSet is the reconciliation unit: one PR's work in one environment.
type ChangeSet struct {
	PR          int
	Environment string
	Exec        Execution
	Gate        GateState
}

// Execution is the plan/apply run for the ChangeSet (edge-triggered facts).
type Execution struct {
	ID     string
	Repo   string
	SHA    string
	LogURL string
	Phase  events.Phase
	Stacks []Stack
}

// Stack is one node. RunStatus holds ONLY the runner-told statuses; the
// gated/safe overlay is derived from the ChangeSet's GateState (see project.go).
type Stack struct {
	Path       string
	Project    string
	RunStatus  events.Status // pending|running|planned|failed|moving (never gated/safe)
	Detail     string
	Categories []events.Category
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
