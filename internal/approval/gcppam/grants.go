package gcppam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

var _ approval.Backend = (*Backend)(nil)

// RequestGrant ensures an open grant exists for req: it lists the entitlement's
// grants and reuses an open one whose justification matches (idempotent
// re-request); otherwise it impersonates the leased requester identity and
// creates a new grant. PAM blocks self-approval and the server only ever holds a
// requester identity, so it can request but never approve.
func (b *Backend) RequestGrant(ctx context.Context, req approval.Request) (approval.Grant, error) {
	ent := b.cfg.entitlementName(req.Class, req.Target)
	if ent == "" {
		return approval.Grant{}, fmt.Errorf("gcppam: no entitlement configured for class %q", req.Class)
	}
	want := justification(req)

	grants, err := b.ListGrants(ctx, req.Class, req.Target)
	if err != nil {
		return approval.Grant{}, err
	}
	leased := map[string]bool{}
	for _, g := range grants {
		if !g.State.Open() {
			continue
		}
		if g.Request.PR == req.PR && g.Request.Environment == req.Environment {
			return g, nil // reuse
		}
		if g.Requester != "" {
			leased[g.Requester] = true
		}
	}

	requester := b.cfg.leaseRequester(req.PR, leased)
	if requester == "" {
		return approval.Grant{}, fmt.Errorf("gcppam: empty requester pool")
	}
	tok, err := b.impersonate(ctx, requester)
	if err != nil {
		return approval.Grant{}, fmt.Errorf("gcppam: impersonate %s: %w", requester, err)
	}

	type createBody struct {
		RequestedDuration string `json:"requestedDuration"`
		Justification     struct {
			UnstructuredJustification string `json:"unstructuredJustification"`
		} `json:"justification"`
	}
	var body createBody
	body.RequestedDuration = b.cfg.duration()
	body.Justification.UnstructuredJustification = want
	payload, err := json.Marshal(body)
	if err != nil {
		return approval.Grant{}, err
	}
	url := fmt.Sprintf("%s/%s/grants", b.cfg.baseURL(), ent)
	rb, err := b.do(ctx, tok, http.MethodPost, url, payload)
	if err != nil {
		return approval.Grant{}, err
	}
	var created struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rb, &created); err != nil {
		return approval.Grant{}, fmt.Errorf("gcppam: unmarshal create: %w", err)
	}
	return approval.Grant{Name: created.Name, State: approval.StateAwaiting, Request: req}, nil
}

// Revoke revokes every open grant on the (class, target) entitlement whose
// justification matches req. Uses the ADC token (the server's own revoke role —
// the requester identity lacks revoke). Idempotent: a no-op when none match.
func (b *Backend) Revoke(ctx context.Context, req approval.Request) error {
	grants, err := b.ListGrants(ctx, req.Class, req.Target)
	if err != nil {
		return err
	}
	tok, err := b.token(ctx)
	if err != nil {
		return fmt.Errorf("gcppam: ADC token: %w", err)
	}
	for _, g := range grants {
		if !g.State.Open() || g.Request.PR != req.PR || g.Request.Environment != req.Environment {
			continue
		}
		url := fmt.Sprintf("%s/%s:revoke", b.cfg.baseURL(), g.Name)
		body := []byte(`{"reason":"post-apply cleanup"}`)
		if _, err := b.do(ctx, tok, http.MethodPost, url, body); err != nil {
			return err
		}
	}
	return nil
}
