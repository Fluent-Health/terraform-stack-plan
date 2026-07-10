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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
)

// Client posts execution lifecycle events to the control-plane server.
type Client struct {
	baseURL string
	token   gauth.TokenFunc // nil = unauthenticated
	hc      *http.Client
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
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
}

// authorize attaches the bearer token to req (a no-op when unauthenticated).
// The fetch is bounded to the client's HTTP timeout so a hung credential
// endpoint cannot stall a best-effort call indefinitely.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.token == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.hc.Timeout)
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

// do builds and sends a JSON POST with bearer auth, returning the raw response.
// The caller is responsible for closing resp.Body. Returns an error on
// transport failure or any non-2xx status.
func (c *Client) do(ctx context.Context, path string, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("post %s: %d: %s", path, resp.StatusCode, body)
	}
	return resp, nil
}

// doRaw sends the same authed JSON POST as do, but returns the HTTP status and
// body for ANY response, erroring only on a transport failure. Callers that
// must distinguish status/code (the fail-closed gate check) use this instead of
// do's non-2xx-collapsing behavior.
func (c *Client) doRaw(ctx context.Context, path string, payload any) (int, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.authorize(ctx, req); err != nil {
		return 0, nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("post %s: read body: %w", path, err)
	}
	return resp.StatusCode, body, nil
}

// post sends a JSON POST with bearer auth. A no-op (nil) when disabled. Returns
// an error on transport failure or any non-2xx status; best-effort callers
// ignore it, the gate check honors it.
func (c *Client) post(ctx context.Context, path string, payload any) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.do(ctx, path, payload)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// postInto posts body to path and decodes a JSON response into out. Same
// request building + non-2xx handling as post; tolerates an empty body.
func (c *Client) postInto(ctx context.Context, path string, body, out any) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := c.do(ctx, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Decode only if there is a body; an empty response is not an error.
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
	return c.post(ctx, "/api/init", in)
}

// Phase narrates a lifecycle phase transition.
func (c *Client) Phase(ctx context.Context, p events.PhaseEvent) error {
	return c.post(ctx, "/api/phase", p)
}

// Update ticks a single stack's status.
func (c *Client) Update(ctx context.Context, u events.Update) error {
	return c.post(ctx, "/api/update", u)
}

// Finalize records the terminal plan state (report, gates, moving/failed).
func (c *Client) Finalize(ctx context.Context, f events.Finalize) error {
	return c.post(ctx, "/api/finalize", f)
}

// LogChunk streams a slice of one stack's output to the server (best-effort).
func (c *Client) LogChunk(ctx context.Context, lc events.LogChunk) error {
	return c.post(ctx, "/api/logs", lc)
}

// GateRevoke asks the server to revoke the grants it requested (best-effort
// post-apply cleanup).
func (c *Client) GateRevoke(ctx context.Context, g events.GateRevoke) error {
	return c.post(ctx, "/api/gate/revoke", g)
}

// ClaimsList returns the current apply-lock claims for an environment.
// Returns nil (no error) when the client is disabled.
func (c *Client) ClaimsList(ctx context.Context, env string) ([]events.Claim, error) {
	var out []events.Claim
	if err := c.postInto(ctx, "/api/claims/list", map[string]string{"environment": env}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimsRelease asks the server to release one stack's claim (stack != "") or
// all of a PR's claims in env (stack == ""). Best-effort; callers may ignore the error.
func (c *Client) ClaimsRelease(ctx context.Context, env string, pr int, stack string) error {
	return c.post(ctx, "/api/claims/release", map[string]any{
		"environment": env,
		"pr":          pr,
		"stack":       stack,
	})
}
