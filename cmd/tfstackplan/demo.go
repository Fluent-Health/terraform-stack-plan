package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// buildDemoApp wires the demo app with a temporary database and in-memory fakes.
func buildDemoApp(ctx context.Context, dbPath string) (*server.App, func(), error) {
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("demo: open store: %w", err)
	}
	cleanup := func() { db.Close() }

	gh := &server.MockGitHub{}
	logsDir := filepath.Join(filepath.Dir(dbPath), "logs")

	app := server.New(db, gh, server.Config{
		WebhookSecret:       "demo-secret",
		GitHubWebhookSecret: "demo-gh-secret",
		PublicBaseURL:       "http://127.0.0.1:8080",
		LogsDir:             logsDir,
	})

	app.Approval = approval.NewFake()
	return app, cleanup, nil
}
