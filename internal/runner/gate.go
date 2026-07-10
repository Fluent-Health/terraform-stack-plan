package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/codes"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// VerdictKind is the typed outcome of the apply-time gate pre-check.
type VerdictKind int

const (
	VerdictSatisfied     VerdictKind = iota // apply may proceed (incl. "no server / nothing gates")
	VerdictNotClassified                    // the PR never finalized a plan
	VerdictNotSatisfied                     // a required grant is not yet active (or an unknown blocking code)
	VerdictUnconfirmable                    // gate state could not be freshly confirmed
	VerdictUnreachable                      // the approval server itself is unreachable
)

// GateVerdict is the typed result of GateCheck. It replaces error-string
// matching: callers switch on Kind. Fail-closed by construction — Allowed() is
// true only for VerdictSatisfied.
type GateVerdict struct {
	Kind      VerdictKind
	Requester string     // leased SA to impersonate; set only when Satisfied
	Code      codes.Code // server-reported code on a non-2xx (empty otherwise)
	Err       error      // underlying error, for logging
}

// Allowed reports whether the apply may proceed.
func (v GateVerdict) Allowed() bool { return v.Kind == VerdictSatisfied }

// GateCheck is the apply-time, fail-closed gate pre-check. No server configured
// ⇒ Satisfied (nothing gates). A transport failure ⇒ Unreachable. A non-2xx ⇒
// the verdict named by the response code (an unknown code fails closed as
// NotSatisfied). 200 ⇒ Satisfied with the leased requester.
func (c *Client) GateCheck(ctx context.Context, g events.GateCheck) GateVerdict {
	if !c.Enabled() {
		return GateVerdict{Kind: VerdictSatisfied}
	}
	resp, err := c.api.CheckGate(ctx, g)
	if err != nil {
		return GateVerdict{Kind: VerdictUnreachable, Err: fmt.Errorf("post /api/gate/check: %w", err)}
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return GateVerdict{Kind: VerdictUnreachable, Err: fmt.Errorf("post /api/gate/check: read body: %w", err)}
	}
	if status/100 == 2 {
		var res struct {
			Requester string `json:"requester"`
		}
		_ = json.Unmarshal(body, &res)
		return GateVerdict{Kind: VerdictSatisfied, Requester: res.Requester}
	}
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	v := GateVerdict{Code: codes.Code(e.Code), Err: fmt.Errorf("gate check: %d: %s", status, e.Message)}
	if e.Code == "" && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		// The auth middleware rejected the caller (plain-text 401/403), not the
		// gate: name the real fix instead of "grant not active" guidance. Still
		// fails closed below.
		v.Err = fmt.Errorf("gate check: %d: API auth rejected — check %s and the server's api_auth principals/scopes", status, EnvAudience)
	}
	switch codes.Code(e.Code) {
	case codes.GateNotClassified:
		v.Kind = VerdictNotClassified
	case codes.GateUnconfirmable:
		v.Kind = VerdictUnconfirmable
	default: // GateNotSatisfied AND any unknown/blank code → fail closed
		v.Kind = VerdictNotSatisfied
	}
	return v
}
