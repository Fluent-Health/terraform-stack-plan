package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// SeedScenario sends a sequence of lifecycle events to baseURL to construct
// a rich, realistic DAG execution for the local demo.
func SeedScenario(ctx context.Context, baseURL string, bearerToken string) (string, error) {
	hc := &http.Client{Timeout: 5 * time.Second}
	execID := fmt.Sprintf("demo-run-%d", time.Now().UnixNano()%1000000)

	// 1. Send Init event with ~8 stacks and dependencies (edges)
	initEv := events.Init{
		ID:          execID,
		Repo:        "Fluent-Health/terraform-stack-plan",
		SHA:         "8a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t",
		PR:          42,
		Environment: "destructive+iam",
		LogURL:      "https://github.com/Fluent-Health/terraform-stack-plan/actions/runs/123456",
		Context:     "apply/destructive+iam",
		Stacks: []events.StackState{
			{Path: "infra/vpc", Status: events.StatusPending},
			{Path: "infra/dns", Status: events.StatusPending},
			{Path: "apps/iam", Status: events.StatusPending},
			{Path: "apps/db", Status: events.StatusPending},
			{Path: "apps/web", Status: events.StatusPending},
			{Path: "apps/api", Status: events.StatusPending},
			{Path: "apps/frontend", Status: events.StatusPending},
			{Path: "apps/cdn", Status: events.StatusPending},
		},
		Edges: []events.Edge{
			{From: "infra/vpc", To: "infra/dns"},
			{From: "infra/vpc", To: "apps/iam"},
			{From: "infra/vpc", To: "apps/db"},
			{From: "apps/iam", To: "apps/web"},
			{From: "apps/db", To: "apps/web"},
			{From: "apps/iam", To: "apps/api"},
			{From: "apps/db", To: "apps/api"},
			{From: "apps/web", To: "apps/frontend"},
			{From: "apps/api", To: "apps/frontend"},
			{From: "apps/frontend", To: "apps/cdn"},
		},
	}

	if err := post(ctx, hc, baseURL+"/api/init", bearerToken, initEv); err != nil {
		return "", fmt.Errorf("init failed: %w", err)
	}

	// 2. Animate statuses with updates
	updates := []events.Update{
		{ID: execID, Stack: "infra/vpc", Status: events.StatusRunning},
		{ID: execID, Stack: "infra/vpc", Status: events.StatusSafe},
		{ID: execID, Stack: "infra/dns", Status: events.StatusRunning},
		{ID: execID, Stack: "infra/dns", Status: events.StatusSafe},
		{ID: execID, Stack: "apps/iam", Status: events.StatusGated},
		{ID: execID, Stack: "apps/db", Status: events.StatusGated},
		{ID: execID, Stack: "apps/web", Status: events.StatusPlanned},
		{ID: execID, Stack: "apps/api", Status: events.StatusPlanned},
		{ID: execID, Stack: "apps/frontend", Status: events.StatusRunning},
	}

	for _, u := range updates {
		if err := post(ctx, hc, baseURL+"/api/update", bearerToken, u); err != nil {
			return "", fmt.Errorf("update failed for %s: %w", u.Stack, err)
		}
	}

	// 3. Finalize run to register gates
	finalizeEv := events.Finalize{
		ID:             execID,
		ReportMarkdown: "## Demo Report\n\n- Infrastructure is fully set up.\n- Application deployments are gated for security.\n",
		Gates: []events.GateTarget{
			{Class: "iam", Target: "proj-a"},
			{Class: "destructive", Target: "proj-a"},
		},
		Projects: map[string]string{
			"apps/iam": "proj-a",
			"apps/db":  "proj-a",
		},
		Categories: map[string][]events.Category{
			"apps/iam": {{Name: "iam", Icon: "🔐"}},
			"apps/db":  {{Name: "destructive", Icon: "💣"}},
		},
		Counts: map[string]events.Counts{
			"apps/iam": {Add: 1},
			"apps/db":  {Destroy: 1},
		},
	}

	if err := post(ctx, hc, baseURL+"/api/finalize", bearerToken, finalizeEv); err != nil {
		return "", fmt.Errorf("finalize failed: %w", err)
	}

	// 4. Force gate check to register awaiting state on fake backends
	gateCheckEv := events.GateCheck{PR: 42, Environment: "destructive+iam"}
	_ = postGateCheck(ctx, hc, baseURL+"/api/gate/check", bearerToken, gateCheckEv)

	// 5. Post some sample logs
	logEv := events.LogChunk{
		ID:    execID,
		Stack: "apps/frontend",
		Data:  "yarn install --silent\nyarn build\nCreating static production bundle...\n",
	}
	if err := post(ctx, hc, baseURL+"/api/logs", bearerToken, logEv); err != nil {
		return "", fmt.Errorf("logs failed: %w", err)
	}

	return execID, nil
}

func post(ctx context.Context, hc *http.Client, url, token string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func postGateCheck(ctx context.Context, hc *http.Client, url, token string, gc events.GateCheck) error {
	body, err := json.Marshal(gc)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Both OK (200) and Conflict (409, gate not approved yet) are expected/valid outcomes for demo
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
