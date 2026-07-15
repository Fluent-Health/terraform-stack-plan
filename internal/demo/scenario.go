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

// SeedScenario sends sequences of lifecycle events to baseURL to construct
// both a Plan execution (showing result diffs) and an Apply execution (showing logs).
// Returns (planID, applyID, error).
func SeedScenario(ctx context.Context, baseURL string) (string, string, error) {
	hc := &http.Client{Timeout: 5 * time.Second}
	planID := fmt.Sprintf("demo-plan-%d", time.Now().UnixNano()%1000000)
	applyID := fmt.Sprintf("demo-apply-%d", (time.Now().UnixNano()+12345)%1000000)

	// Wait for the server to be ready before starting
	readyURL := baseURL + "/ready"
	var ready bool
	var lastErr error
	for i := 0; i < 20; i++ {
		req, err := http.NewRequestWithContext(ctx, "GET", readyURL, nil)
		if err == nil {
			resp, err := hc.Do(req)
			if err == nil {
				if resp.StatusCode == http.StatusOK {
					resp.Body.Close()
					ready = true
					break
				}
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", "", fmt.Errorf("context cancelled or timed out: %w (last error: %v)", ctx.Err(), lastErr)
			}
			return "", "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		if lastErr != nil {
			return "", "", fmt.Errorf("server at %s not ready after timeout: %w", baseURL, lastErr)
		}
		return "", "", fmt.Errorf("server at %s not ready after timeout", baseURL)
	}

	// ==========================================
	// 1. Seed Plan Execution (Result Tab / Diff)
	// ==========================================
	planInitEv := events.Init{
		ID:          planID,
		Repo:        "Fluent-Health/terraform-stack-plan",
		SHA:         "8a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t",
		PR:          42,
		Environment: "destructive+iam",
		LogURL:      "https://github.com/Fluent-Health/terraform-stack-plan/actions/runs/123456",
		Context:     "plan", // Plan context (LogDefault = false when finished)
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

	if err := post(ctx, hc, baseURL+"/api/init", planInitEv); err != nil {
		return "", "", fmt.Errorf("plan init failed: %w", err)
	}

	// Narrate the real CI phase sequence, including the double `warming` a real
	// run emits (the early CI tick and run plan's provider-cache warm) with
	// linting between — the lifecycle fold must coalesce both into one
	// "prepare" segment. This keeps demo mode a regression playground for the
	// repeated-segment bug.
	for _, ph := range []events.Phase{events.PhaseWarming, events.PhaseLinting, events.PhaseWarming, events.PhasePlanning} {
		if err := post(ctx, hc, baseURL+"/api/phase", events.PhaseEvent{ID: planID, Phase: ph}); err != nil {
			return "", "", fmt.Errorf("plan phase %s failed: %w", ph, err)
		}
	}

	planUpdates := []events.Update{
		{ID: planID, Stack: "infra/vpc", Status: events.StatusNochange},
		{ID: planID, Stack: "infra/dns", Status: events.StatusNochange},
		{ID: planID, Stack: "apps/iam", Status: events.StatusPlanned},
		{ID: planID, Stack: "apps/db", Status: events.StatusPlanned},
		{ID: planID, Stack: "apps/web", Status: events.StatusPlanned},
		{ID: planID, Stack: "apps/api", Status: events.StatusPlanned},
		{ID: planID, Stack: "apps/frontend", Status: events.StatusNochange},
		{ID: planID, Stack: "apps/cdn", Status: events.StatusNochange},
	}

	for _, u := range planUpdates {
		if err := post(ctx, hc, baseURL+"/api/update", u); err != nil {
			return "", "", fmt.Errorf("plan update failed for %s: %w", u.Stack, err)
		}
	}

	planFinalizeEv := events.Finalize{
		ID:             planID,
		ReportMarkdown: "## Plan Summary Report\n\n- No-change: 4 stacks\n- Planned (Gated): 2 stacks\n- Planned (Clean): 2 stacks\n",
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
		StackReports: map[string]string{
			"apps/iam": "```diff\n+ google_project_iam_member.x\n+ project = \"proj-a\"\n+ role    = \"roles/editor\"\n```\n",
			"apps/db":  "```diff\n- google_sql_database_instance.prod\n- name    = \"db-prod\"\n```\n",
		},
	}

	for _, ph := range []events.Phase{events.PhaseClassify, events.PhaseReport} {
		if err := post(ctx, hc, baseURL+"/api/phase", events.PhaseEvent{ID: planID, Phase: ph}); err != nil {
			return "", "", fmt.Errorf("plan phase %s failed: %w", ph, err)
		}
	}

	if err := post(ctx, hc, baseURL+"/api/finalize", planFinalizeEv); err != nil {
		return "", "", fmt.Errorf("plan finalize failed: %w", err)
	}

	// ==========================================
	// 2. Seed Apply Execution (Log Tab / Output)
	// ==========================================
	applyInitEv := events.Init{
		ID:          applyID,
		Repo:        "Fluent-Health/terraform-stack-plan",
		SHA:         "8a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t",
		PR:          42,
		Environment: "destructive+iam",
		LogURL:      "https://github.com/Fluent-Health/terraform-stack-plan/actions/runs/123456",
		Context:     "apply/destructive+iam", // Apply context (LogDefault = true)
		Stacks:      planInitEv.Stacks,
		Edges:       planInitEv.Edges,
	}

	if err := post(ctx, hc, baseURL+"/api/init", applyInitEv); err != nil {
		return "", "", fmt.Errorf("apply init failed: %w", err)
	}

	for _, ph := range []events.Phase{events.PhaseWarming, events.PhaseMoves, events.PhaseApplying} {
		if err := post(ctx, hc, baseURL+"/api/phase", events.PhaseEvent{ID: applyID, Phase: ph}); err != nil {
			return "", "", fmt.Errorf("apply phase %s failed: %w", ph, err)
		}
	}

	applyUpdates := []events.Update{
		{ID: applyID, Stack: "infra/vpc", Status: events.StatusRunning},
		{ID: applyID, Stack: "infra/vpc", Status: events.StatusSafe},
		{ID: applyID, Stack: "infra/dns", Status: events.StatusRunning},
		{ID: applyID, Stack: "infra/dns", Status: events.StatusSafe},
		{ID: applyID, Stack: "apps/iam", Status: events.StatusGated},
		{ID: applyID, Stack: "apps/db", Status: events.StatusGated},
		{ID: applyID, Stack: "apps/web", Status: events.StatusPlanned},
		{ID: applyID, Stack: "apps/api", Status: events.StatusPlanned},
		{ID: applyID, Stack: "apps/frontend", Status: events.StatusRunning},
	}

	for _, u := range applyUpdates {
		if err := post(ctx, hc, baseURL+"/api/update", u); err != nil {
			return "", "", fmt.Errorf("apply update failed for %s: %w", u.Stack, err)
		}
	}

	applyFinalizeEv := events.Finalize{
		ID:             applyID,
		ReportMarkdown: "## Apply Report\n\n- Infrastructure is fully set up.\n- Application deployments are gated for security.\n",
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

	if err := post(ctx, hc, baseURL+"/api/finalize", applyFinalizeEv); err != nil {
		return "", "", fmt.Errorf("apply finalize failed: %w", err)
	}

	// Force gate check to register awaiting state on fake backends
	gateCheckEv := events.GateCheck{PR: 42, Environment: "destructive+iam"}
	if err := postGateCheck(ctx, hc, baseURL+"/api/gate/check", gateCheckEv); err != nil {
		return "", "", fmt.Errorf("gate check failed: %w", err)
	}

	// Post some sample logs to apps/frontend (representing apply logging)
	logEv := events.LogChunk{
		ID:    applyID,
		Stack: "apps/frontend",
		Data:  "\x1b[32m✔ yarn install --silent\x1b[0m\n\x1b[34mℹ yarn build\x1b[0m\nCreating static production bundle...\n\x1b[32m✔ Compiled successfully!\x1b[0m\n",
	}
	if err := post(ctx, hc, baseURL+"/api/logs", logEv); err != nil {
		return "", "", fmt.Errorf("apply logs failed: %w", err)
	}

	return planID, applyID, nil
}

func post(ctx context.Context, hc *http.Client, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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

func postGateCheck(ctx context.Context, hc *http.Client, url string, gc events.GateCheck) error {
	body, err := json.Marshal(gc)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
