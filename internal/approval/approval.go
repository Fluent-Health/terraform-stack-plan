// Package approval defines the approval-gate backend abstraction. The server
// asks a Backend to request grants for a change's (class, target) gates, lists
// their state to decide whether a gate is satisfied, and revokes them after
// apply. The server never holds standing privilege — it only requests; humans
// approve in the backing provider. gcp-pam is the first real backend; Fake backs
// the tests.
package approval

import "context"

// GrantState is the lifecycle of an approval grant, normalised across backends.
type GrantState string

const (
	StateAwaiting   GrantState = "AWAITING"   // requested; awaiting an approver
	StateActivating GrantState = "ACTIVATING" // approved; becoming active
	StateActive     GrantState = "ACTIVE"     // approved and active
	StateDenied     GrantState = "DENIED"
	StateRevoked    GrantState = "REVOKED"
	StateExpired    GrantState = "EXPIRED"
)

// Open reports whether the state is a live, non-terminal grant.
func (s GrantState) Open() bool {
	switch s {
	case StateAwaiting, StateActivating, StateActive:
		return true
	}
	return false
}

// Request correlates a gate to a change: a classification class against a target
// (e.g. a cloud project/account), for a specific PR and environment.
type Request struct {
	Class       string
	Target      string
	PR          int
	Environment string
}

// Grant is a backend's record of an approval grant for a Request.
type Grant struct {
	Name    string // backend-assigned resource name/id
	State   GrantState
	Request Request // correlation, parsed back from the grant by the backend
}

// Backend is an approval provider. The server requests grants and reads their
// state; it can never approve (that is a human action in the provider).
type Backend interface {
	// RequestGrant ensures an open grant exists for req — create-or-reuse,
	// idempotent: a second request for the same (class,target,pr,environment)
	// returns the existing open grant rather than creating a duplicate.
	RequestGrant(ctx context.Context, req Request) (Grant, error)
	// ListGrants returns the grants on a (class, target) entitlement across all
	// PRs (the caller filters by Request).
	ListGrants(ctx context.Context, class, target string) ([]Grant, error)
	// Revoke revokes the open grants matching req. Idempotent (a no-op when none).
	Revoke(ctx context.Context, req Request) error
}
