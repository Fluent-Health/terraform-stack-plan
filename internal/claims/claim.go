// Package claims is the pure event-sourced domain core for the apply-lock claim
// ledger. It follows the Decider pattern: Decide emits Events, Evolve folds
// them into a ClaimSet. No I/O, no store imports — this is a pure leaf package.
package claims

import "time"

// Lease is the duration for which a claim is valid before it must be renewed.
func Lease() time.Duration { return 30 * time.Minute }

// Claim holds the ownership record for a single stack slot.
type Claim struct {
	PR        int
	ExpiresAt time.Time
}

// ClaimSet is the folded state of the claim ledger: stack path → Claim.
type ClaimSet map[string]Claim

// Empty returns an empty ClaimSet (the initial state for the fold).
func Empty() ClaimSet { return ClaimSet{} }

// --- Command sum type ---

// Command is a request to change claim state.
type Command interface{ isCommand() }

// AcquireClaim requests that pr acquire a claim on the given stacks.
type AcquireClaim struct {
	PR     int
	Stacks []string
	Now    time.Time
}

// RenewClaim requests that the existing claim by pr be renewed.
type RenewClaim struct {
	PR  int
	Now time.Time
}

// ReleaseClaim requests that all stacks claimed by pr be released.
type ReleaseClaim struct {
	PR int
}

// ReleaseClaimStack requests that a single stack claimed by pr be released.
type ReleaseClaimStack struct {
	PR    int
	Stack string
}

func (AcquireClaim) isCommand()      {}
func (RenewClaim) isCommand()        {}
func (ReleaseClaim) isCommand()      {}
func (ReleaseClaimStack) isCommand() {}

// --- Event sum type ---

// Event is a past-tense domain fact produced by Decide and folded by Evolve.
type Event interface{ isEvent() }

// ClaimAcquired records that pr has acquired claims on the given stacks.
type ClaimAcquired struct {
	PR        int
	Stacks    []string
	ExpiresAt time.Time
}

// ClaimRenewed records that pr's claims have been extended to ExpiresAt.
type ClaimRenewed struct {
	PR        int
	ExpiresAt time.Time
}

// ClaimReleased records that all stacks claimed by pr have been released.
type ClaimReleased struct {
	PR int
}

// ClaimStackReleased records that a single stack claimed by pr has been released.
type ClaimStackReleased struct {
	PR    int
	Stack string
}

func (ClaimAcquired) isEvent()      {}
func (ClaimRenewed) isEvent()       {}
func (ClaimReleased) isEvent()      {}
func (ClaimStackReleased) isEvent() {}
