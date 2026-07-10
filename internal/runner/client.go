// Package runner is the CI-side driver: it invokes the same terramate scripts a
// human runs, captures per-stack output, renders + classifies in-process, and
// reports progress to the control-plane server over the typed events protocol.
//
// This file is the server client. All reporting is best-effort by design — a
// down or absent server degrades the build to "no live progress", never to
// failure — except the apply-time gate check (see gate.go), which is
// fail-closed. An empty server URL disables the client entirely, so local runs
// and the no-op `run tick` need no server.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
)

// Client posts execution lifecycle events to the control-plane server. It
// wraps the OpenAPI-generated client (internal/api — the wire contract lives
// in api/openapi.yaml) with the runner's calling conventions: disabled on an
// empty URL, best-effort bearer minting, and non-2xx collapsed to errors.
type Client struct {
	baseURL string
	api     *api.Client     // nil only when disabled
	token   gauth.TokenFunc // nil = unauthenticated
	timeout time.Duration
}

// NewClient builds an unauthenticated client for the server at baseURL (empty
// disables it). Authenticated callers use NewClientTokenSource (Google OIDC).
// A short timeout keeps a slow/down server from stalling the build.
func NewClient(baseURL string) *Client {
	return NewClientTokenSource(baseURL, nil)
}

// NewClientTokenSource builds a client whose requests carry bearer tokens from
// token — the Google OIDC path (ID tokens minted from ambient credentials).
func NewClientTokenSource(baseURL string, token gauth.TokenFunc) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		timeout: 10 * time.Second,
	}
	if c.baseURL != "" {
		// NewClient cannot fail here: the only error paths are option
		// constructors, and neither option used errors.
		c.api, _ = api.NewClient(c.baseURL,
			api.WithHTTPClient(&http.Client{Timeout: c.timeout}),
			api.WithRequestEditorFn(c.authorize))
	}
	return c
}

// authorize attaches the bearer token to req (a no-op when unauthenticated).
// The fetch is bounded to the client's timeout so a hung credential endpoint
// cannot stall a best-effort call indefinitely.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.token == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	tok, err := c.token(ctx)
	if err != nil {
		return fmt.Errorf("api token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// Enabled reports whether a server is configured. When false, every call is a
// no-op returning nil.
func (c *Client) Enabled() bool { return c.baseURL != "" }

// finish collapses a generated-client call to the runner's error convention:
// nil on 2xx, an error naming the path on transport failure or any non-2xx
// status (with the response body in the message).
func (c *Client) finish(path string, resp *http.Response, err error) error {
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post %s: %d: %s", path, resp.StatusCode, body)
	}
	return nil
}

// finishInto is finish plus a JSON decode of a 2xx body into out (an empty
// body is not an error).
func (c *Client) finishInto(path string, resp *http.Response, err error, out any) error {
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post %s: %d: %s", path, resp.StatusCode, body)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("post %s: read body: %w", path, err)
	}
	if len(b) > 0 && out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("post %s: decode: %w", path, err)
		}
	}
	return nil
}

// Init registers the execution and its changed subgraph.
func (c *Client) Init(ctx context.Context, in events.Init) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.InitExecution(ctx, in)
	return c.finish("/api/init", resp, err)
}

// Phase narrates a lifecycle phase transition.
func (c *Client) Phase(ctx context.Context, p events.PhaseEvent) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.ReportPhase(ctx, p)
	return c.finish("/api/phase", resp, err)
}

// Update ticks a single stack's status.
func (c *Client) Update(ctx context.Context, u events.Update) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.UpdateStack(ctx, u)
	return c.finish("/api/update", resp, err)
}

// Finalize records the terminal plan state (report, gates, moving/failed).
func (c *Client) Finalize(ctx context.Context, f events.Finalize) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.FinalizeExecution(ctx, f)
	return c.finish("/api/finalize", resp, err)
}

// LogChunk streams a slice of one stack's output to the server (best-effort).
func (c *Client) LogChunk(ctx context.Context, lc events.LogChunk) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.AppendLogs(ctx, lc)
	return c.finish("/api/logs", resp, err)
}

// GateRevoke asks the server to revoke the grants it requested (best-effort
// post-apply cleanup).
func (c *Client) GateRevoke(ctx context.Context, g events.GateRevoke) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.RevokeGate(ctx, g)
	return c.finish("/api/gate/revoke", resp, err)
}

// ClaimsList returns the current apply-lock claims for an environment.
// Returns nil (no error) when the client is disabled.
func (c *Client) ClaimsList(ctx context.Context, env string) ([]events.Claim, error) {
	if !c.Enabled() {
		return nil, nil
	}
	var out []events.Claim
	resp, err := c.api.ListClaims(ctx, api.ClaimsListRequest{Environment: env})
	if err := c.finishInto("/api/claims/list", resp, err, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimsRelease asks the server to release one stack's claim (stack != "") or
// all of a PR's claims in env (stack == ""). Best-effort; callers may ignore the error.
func (c *Client) ClaimsRelease(ctx context.Context, env string, pr int, stack string) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.api.ReleaseClaims(ctx, api.ClaimsReleaseRequest{
		Environment: env,
		Pr:          pr,
		Stack:       stack,
	})
	return c.finish("/api/claims/release", resp, err)
}
