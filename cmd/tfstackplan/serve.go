package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
func buildServeApp(ctx context.Context, cfg *config.Config, secret, ghWebhookSecret string, creds credsFactory) (*server.App, func(), error) {
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

	reconcilerCore := s.ReconcilerCore
	if reconcilerCore {
		if q, qerr := store.IsQuiescent(db); qerr != nil {
			fmt.Fprintf(os.Stderr, "serve: quiescence check failed (%v) — using legacy engine\n", qerr)
			reconcilerCore = false
		} else if !q {
			fmt.Fprintln(os.Stderr, "serve: reconciler_core requested but store not quiescent (PRs in flight) — using legacy engine until drained")
			reconcilerCore = false
		}
	}

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

	logsDir := defaultLogsDir(s.LogsDir, s.DBPath)
	fmt.Fprintf(os.Stderr, "tfstackplan serve: log buffers in %s\n", logsDir)

	app := server.New(db, gh, server.Config{
		WebhookSecret:       secret,
		GitHubWebhookSecret: ghWebhookSecret,
		PublicBaseURL:       s.PublicBaseURL,
		UseChecks:           s.UseChecks,
		ReconcilerCore:      reconcilerCore,
		ApplyLock:           s.ApplyLock,
		GroupDepth:          groupDepth(s),
		GroupPattern:        groupPattern(s),
		LogsDir:             logsDir,
		PushServiceAccount:  pubsubSA(s),
	})

	if s.PubSub != nil {
		aud := s.PubSub.Audience
		if aud == "" {
			aud = strings.TrimRight(s.PublicBaseURL, "/") + "/pubsub/push"
		}
		app.PushVerifier = gcpOIDCVerifier(aud)
	}

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

// pubsubSA returns the configured Pub/Sub push service-account email ("" if unset).
func pubsubSA(s *config.ServeConfig) string {
	if s.PubSub != nil {
		return s.PubSub.ServiceAccount
	}
	return ""
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

// defaultLogsDir resolves the per-stack log buffer dir: an explicit logs_dir
// wins; otherwise default to <dir(db_path)>/logs so log ingestion (live tail,
// excerpt, offload) can never be silently disabled by an omitted knob.
func defaultLogsDir(configured, dbPath string) string {
	if configured != "" {
		return configured
	}
	return filepath.Join(filepath.Dir(dbPath), "logs")
}

// runServe loads config, builds the app, starts the reconcile loop, and serves.
func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFilename, "HCL config file")
	addr := fs.String("addr", ":8080", "listen address")
	checkQ := fs.Bool("check-quiescent", false, "report whether the store is drained (no in-flight gates) and exit 0/1")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	if *checkQ {
		if cfg.Serve == nil || cfg.Serve.DBPath == "" {
			fmt.Fprintln(os.Stderr, "tfstackplan serve: db_path required for --check-quiescent")
			return 1
		}
		db, err := store.Open(cfg.Serve.DBPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan serve: open store:", err)
			return 1
		}
		defer db.Close()
		q, err := store.IsQuiescent(db)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan serve: quiescence check:", err)
			return 1
		}
		if q {
			fmt.Println("quiescent")
			return 0
		}
		fmt.Println("in-flight")
		return 1
	}
	secret := ""
	if cfg.Serve != nil && cfg.Serve.WebhookSecretEnv != "" {
		secret = os.Getenv(cfg.Serve.WebhookSecretEnv)
	}
	ghWebhookSecret := ""
	if cfg.Serve != nil && cfg.Serve.GitHubWebhookSecretEnv != "" {
		ghWebhookSecret = os.Getenv(cfg.Serve.GitHubWebhookSecretEnv)
	}
	ctx := context.Background()
	app, cleanup, err := buildServeApp(ctx, cfg, secret, ghWebhookSecret, gcpCreds)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	defer cleanup()

	go app.ReconcileLoop(ctx, 30*time.Second)
	go app.OrphanSweepLoop(ctx, 5*time.Minute)
	go app.CleanLogBuffers(24 * time.Hour)

	fmt.Fprintf(os.Stderr, "tfstackplan serve: listening on %s\n", *addr)
	srv := &http.Server{Addr: *addr, Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	return 0
}
