package runner

import (
	"context"
	"fmt"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// GateCheck is the apply-time, fail-closed gate pre-check. Returns the leased
// requester SA (when the server reports one) so the caller can impersonate it.
// No server ⇒ ("", nil) — nothing gates. A 409, any other non-2xx, or an
// unreachable configured server ⇒ error (apply blocks).
func (c *Client) GateCheck(ctx context.Context, g events.GateCheck) (string, error) {
	if !c.Enabled() {
		return "", nil
	}
	var res struct {
		Requester string `json:"requester"`
	}
	if err := c.postInto(ctx, "/api/gate/check", g, &res); err != nil {
		return "", fmt.Errorf("apply gate not satisfied (fail-closed): %w", err)
	}
	return res.Requester, nil
}
