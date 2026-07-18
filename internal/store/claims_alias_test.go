package store

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// TestClaimIsEventsAlias fails to compile if store.Claim is a distinct named type
// rather than an alias of events.Claim.
func TestClaimIsEventsAlias(t *testing.T) {
	var c Claim = events.Claim{Environment: "nonprod", OwnerPR: 7}
	var _ events.Claim = c // compiles only if alias, not a distinct named type
	if c.OwnerPR != 7 {
		t.Fatalf("OwnerPR = %d", c.OwnerPR)
	}
}
