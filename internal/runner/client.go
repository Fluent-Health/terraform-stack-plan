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
	"sync"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// Client posts execution lifecycle events to the control-plane server.
type Client struct {
	baseURL     string
	secret      string
	iapAudience string // OAuth2 client ID for IAP; empty = no IAP
	hc          *http.Client
	// lazy IAP token source; initialized once on first use
	iapOnce sync.Once
	iapTS   oauth2.TokenSource
	iapErr  error
}

// NewClient builds a client for the server at baseURL (empty disables it) using
// the given bearer secret. A short timeout keeps a slow/down server from
// stalling the build.
func NewClient(baseURL, secret string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
}

// iapToken returns a short-lived GCP IAP OIDC token for c.iapAudience using
// Application Default Credentials. The underlying TokenSource is created once
// and reused (it caches the token and refreshes automatically).
func (c *Client) iapToken(ctx context.Context) (string, error) {
	c.iapOnce.Do(func() {
		c.iapTS, c.iapErr = idtoken.NewTokenSource(context.Background(), c.iapAudience)
	})
	if c.iapErr != nil {
		return "", fmt.Errorf("iap token source: %w", c.iapErr)
	}
	tok, err := c.iapTS.Token()
	if err != nil {
		return "", fmt.Errorf("iap token: %w", err)
	}
	return tok.AccessToken, nil
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
	if c.iapAudience != "" {
		// IAP-fronted: Authorization carries the IAP OIDC token; the webhook
		// secret moves to X-Tfstackplan-Token (IAP strips Authorization before
		// forwarding, so the server reads the secret from the custom header).
		tok, err := c.iapToken(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if c.secret != "" {
			req.Header.Set("X-Tfstackplan-Token", c.secret)
		}
	} else if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
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
