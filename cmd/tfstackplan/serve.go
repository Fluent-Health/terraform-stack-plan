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
	"github.com/Fluent-Health/terraform-stack-plan/internal/demo"
	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
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
		Environment:         serverEnvironment(cfg),
		GroupDepth:          groupDepth(s),
		GroupPattern:        groupPattern(s),
		LogsDir:             logsDir,
		PushServiceAccount:  pubsubSA(s),
		APIPrincipals:       apiPrincipals(s),
		Progress:            cfg.Progress,
	})

	if s.APIAuth != nil {
		aud := s.APIAuth.Audience
		if aud == "" {
			aud = strings.TrimRight(s.PublicBaseURL, "/")
		}
		if aud == "" {
			cleanup()
			return nil, nil, fmt.Errorf("serve: api_auth needs an audience (set api_auth.audience or public_base_url)")
		}
		verify, err := gauth.Verifier(ctx, append([]string{aud}, s.APIAuth.ExtraAudiences...))
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("serve: api_auth verifier: %w", err)
		}
		app.APIVerifier = verify
	}

	if s.PubSub != nil {
		aud := s.PubSub.Audience
		if aud == "" {
			aud = strings.TrimRight(s.PublicBaseURL, "/") + "/pubsub/push"
		}
		verify, err := gauth.Verifier(ctx, []string{aud})
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("serve: pubsub verifier: %w", err)
		}
		app.PushVerifier = verify
	}

	if s.Executor != nil && s.Executor.Backend == "cloudbuild" {
		token, _, err := creds(ctx)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("serve: gcp credentials for executor: %w", err)
		}
		app.Executor = executor.NewCloudBuild(s.Executor.Project, s.Executor.Region, s.Executor.Triggers, executor.TokenFunc(token))
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

// apiPrincipals flattens the api_auth principal blocks into the server's
// email → scopes map (emails lowercased; nil when the block is absent).
func apiPrincipals(s *config.ServeConfig) map[string][]string {
	if s.APIAuth == nil {
		return nil
	}
	m := make(map[string][]string, len(s.APIAuth.Principals))
	for _, p := range s.APIAuth.Principals {
		m[strings.ToLower(p.Email)] = p.Scopes
	}
	return m
}

// serverEnvironment returns this tier's environment from the shared server{}
// block ("" if unset — run triggering stays disarmed then).
func serverEnvironment(cfg *config.Config) string {
	if cfg.Server != nil {
		return cfg.Server.Environment
	}
	return ""
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
	demoFlag := fs.Bool("demo", false, "boot in credential-free demo mode with seeded data")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var app *server.App
	var cleanup func()
	var err error
	ctx := context.Background()

	if *demoFlag {
		fmt.Fprintln(os.Stderr, "tfstackplan serve: starting in credential-free demo mode...")
		var tempDir string
		tempDir, err = os.MkdirTemp("", "tfstackplan-demo-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan serve: create temp dir:", err)
			return 1
		}
		dbPath := filepath.Join(tempDir, "demo.db")
		app, cleanup, err = buildDemoApp(ctx, dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan serve demo:", err)
			return 1
		}

		// Run a background task to seed the scenario after a brief server startup delay
		go func() {
			time.Sleep(150 * time.Millisecond)
			hostPort := *addr
			if strings.HasPrefix(hostPort, ":") {
				hostPort = "127.0.0.1" + hostPort
			}
			planID, applyID, err := demo.SeedScenario(ctx, "http://"+hostPort, "demo-secret")
			if err != nil {
				fmt.Fprintf(os.Stderr, "demo: seed scenario failed: %v\n", err)
				return
			}
			fmt.Fprintf(os.Stderr, "\n=================================================================\n")
			fmt.Fprintf(os.Stderr, "DEMO MODE READY!\n")
			fmt.Fprintf(os.Stderr, "Seeded Plan ID:      %s\n", planID)
			fmt.Fprintf(os.Stderr, "Seeded Apply ID:     %s\n", applyID)
			fmt.Fprintf(os.Stderr, "Browse Plan (Diff):  http://%s/live/%s\n", hostPort, planID)
			fmt.Fprintf(os.Stderr, "Browse Apply (Log):  http://%s/live/%s\n", hostPort, applyID)
			fmt.Fprintf(os.Stderr, "Webhook URL:         http://%s/webhook\n", hostPort)
			fmt.Fprintf(os.Stderr, "Ready URL:           http://%s/ready\n", hostPort)
			fmt.Fprintf(os.Stderr, "=================================================================\n\n")
		}()
	} else {
		var cfg *config.Config
		cfg, err = config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
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
		app, cleanup, err = buildServeApp(ctx, cfg, secret, ghWebhookSecret, gcpCreds)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
			return 1
		}
	}
	defer cleanup()

	go app.ReconcileLoop(ctx, 30*time.Second)
	go app.OrphanSweepLoop(ctx, 5*time.Minute)
	go app.ClaimsSweepLoop(ctx, time.Minute)
	go app.CleanLogBuffers(24 * time.Hour)

	fmt.Fprintf(os.Stderr, "tfstackplan serve: listening on %s\n", *addr)
	srv := &http.Server{Addr: *addr, Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan serve:", err)
		return 1
	}
	return 0
}
