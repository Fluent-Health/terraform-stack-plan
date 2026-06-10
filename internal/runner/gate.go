package runner

import (
	"context"
	"fmt"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// GateCheck is the apply-time, fail-closed gate pre-check. With no server
// configured it is a no-op (nothing gates). With a server it returns nil only
// when the gate is satisfied (200); a 409, any other non-2xx, or an unreachable
// server returns an error — so the apply path blocks rather than proceeding on
// an unverified gate.
func (c *Client) GateCheck(ctx context.Context, g events.GateCheck) error {
	if err := c.post(ctx, "/api/gate/check", g); err != nil {
		return fmt.Errorf("apply gate not satisfied (fail-closed): %w", err)
	}
	return nil
}
