package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval/gcppam"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// credsFactory returns the gcp-pam credential funcs; injected so the wiring is
// testable without live GCP.
type credsFactory func(ctx context.Context) (gcppam.TokenFunc, gcppam.ImpersonateFunc, error)

// gcppamConfig builds the gcp-pam backend config from the serve.approval block
// and the per-class entitlement bindings.
func gcppamConfig(cfg *config.Config) gcppam.Config {
	gc := gcppam.Config{Entitlements: map[string]string{}, EntitlementScopes: map[string]string{}}
	if a := cfg.Serve.Approval; a != nil {
		gc.Location = a.Location
		gc.Duration = a.Duration
		gc.RequesterPool = a.RequesterPool
	}
	for _, c := range cfg.Classes {
		if c.Entitlement != "" {
			gc.Entitlements[c.Name] = c.Entitlement
			if c.EntitlementScope != "" {
				gc.EntitlementScopes[c.Name] = c.EntitlementScope
			}
		}
	}
	return gc
}

// buildServeApp wires config → store + GitHub client + gcp-pam backend → App.
// Returns a cleanup that closes the store. The GCP creds are injected.
func buildServeApp(ctx context.Context, cfg *config.Config, secret string, creds credsFactory) (*server.App, func(), error) {
	if cfg.Serve == nil {
		return nil, nil, fmt.Errorf("serve: no `serve {}` block in config")
	}
	s := cfg.Serve
	if s.DBPath == "" {
		return nil, nil, fmt.Errorf("serve: db_path is required")
	}
	db, err := store.Open(s.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("serve: open store: %w", err)
	}
	cleanup := func() { db.Close() }

	if s.GitHubApp == nil {
		cleanup()
		return nil, nil, fmt.Errorf("serve: github_app block is required")
	}
	key, err := os.ReadFile(s.GitHubApp.PrivateKeyPath)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("serve: read github app key: %w", err)
	}
	gh, err := server.NewRealClient(s.GitHubApp.AppID, s.GitHubApp.InstallationID, key)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("serve: github client: %w", err)
	}

	app := server.New(db, gh, server.Config{
		WebhookSecret: secret,
		PublicBaseURL: s.PublicBaseURL,
		UseChecks:     s.UseChecks,
		GroupDepth:    groupDepth(s),
		GroupPattern:  groupPattern(s),
		LogsDir:       s.LogsDir,
	})

	if s.Approval != nil && s.Approval.Backend == "gcp-pam" {
		token, impersonate, err := creds(ctx)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("serve: gcp credentials: %w", err)
		}
		var b approval.Backend = gcppam.New(gcppamConfig(cfg), token, impersonate)
		app.Approval = b
	}

	if s.Objects != nil && (s.Objects.Backend == "" || s.Objects.Backend == "gcs") && s.Objects.Bucket != "" {
		token, _, err := creds(ctx)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("serve: gcp creds for objects: %w", err)
		}
		app.Objects = newGCSObjectStore(token, s.Objects.Bucket, s.Objects.Prefix, "")
	}
	return app, cleanup, nil
}

// groupDepth returns the configured live-DAG grouping depth (0 if unset).
func groupDepth(s *config.ServeConfig) int {
	if s.Group != nil {
		return s.Group.Depth
	}
	return 0
}

// groupPattern returns the configured live-DAG grouping regexp ("" if unset).
func groupPattern(s *config.ServeConfig) string {
	if s.Group != nil {
		return s.Group.Pattern
	}
	return ""
}

// runServe loads config, builds the app, starts the reconcile loop, and serves.
func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFilename, "HCL config file")
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	secret := ""
	if cfg.Serve != nil && cfg.Serve.WebhookSecretEnv != "" {
		secret = os.Getenv(cfg.Serve.WebhookSecretEnv)
	}
	ctx := context.Background()
	app, cleanup, err := buildServeApp(ctx, cfg, secret, gcpCreds)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	defer cleanup()

	go app.ReconcileLoop(ctx, 30*time.Second)

	fmt.Fprintf(os.Stderr, "tfstackplan serve: listening on %s\n", *addr)
	srv := &http.Server{Addr: *addr, Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	return 0
}
