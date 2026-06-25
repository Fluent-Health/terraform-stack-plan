//go:build screenshots

package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/Fluent-Health/terraform-stack-plan/internal/demo"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
)

func TestCaptureScreenshots(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "demo.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	gh := &server.MockGitHub{}
	app := server.New(db, gh, server.Config{
		WebhookSecret: "capture-secret",
		LogsDir:       filepath.Join(tempDir, "logs"),
	})
	app.Approval = approval.NewFake()

	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	// Mint a valid API token for seeding mutations
	apiToken, err := jwtutil.Make("capture-secret", "runner", "api", time.Hour)
	if err != nil {
		t.Fatalf("mint api token: %v", err)
	}

	// Mint a valid View token for rendering GET routes
	viewToken, err := jwtutil.Make("capture-secret", "viewer", "view", time.Hour)
	if err != nil {
		t.Fatalf("mint view token: %v", err)
	}

	// Seed the scenario
	execID, err := demo.SeedScenario(context.Background(), srv.URL, apiToken)
	if err != nil {
		t.Fatalf("seed scenario: %v", err)
	}

	// Chromedp allocation and context creation
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Headless,
	)
	if cp := os.Getenv("CHROME_PATH"); cp != "" {
		opts = append(opts, chromedp.ExecPath(cp))
	}

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	outDir := "../docs/images"
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatalf("mkdir docs/images: %v", err)
	}

	// 1. Capture DAG View Page (Full Page)
	liveURL := srv.URL + "/live/" + execID + "?token=" + viewToken
	var buf []byte
	err = chromedp.Run(ctx,
		chromedp.Navigate(liveURL),
		chromedp.WaitVisible("svg", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second), // Wait for SVG transitions to settle
		chromedp.Screenshot("body", &buf, chromedp.NodeVisible),
	)
	if err != nil {
		t.Fatalf("capture serve-dag screenshot failed: %v. Please verify Chromium/Chrome is installed or set CHROME_PATH.", err)
	}

	dagPath := filepath.Join(outDir, "serve-dag.png")
	if err := os.WriteFile(dagPath, buf, 0644); err != nil {
		t.Fatalf("write serve-dag.png failed: %v", err)
	}
	t.Logf("Created: %s", dagPath)

	// 2. Capture Gated Environment Approvals Panel
	var bufGate []byte
	err = chromedp.Run(ctx,
		chromedp.Navigate(liveURL),
		chromedp.WaitVisible(".panel", chromedp.ByQuery),
		chromedp.Sleep(1*time.Second),
		chromedp.Screenshot(".panel", &bufGate, chromedp.NodeVisible),
	)
	if err != nil {
		t.Fatalf("capture serve-gate screenshot failed: %v", err)
	}

	gatePath := filepath.Join(outDir, "serve-gate.png")
	if err := os.WriteFile(gatePath, bufGate, 0644); err != nil {
		t.Fatalf("write serve-gate.png failed: %v", err)
	}
	t.Logf("Created: %s", gatePath)
}
