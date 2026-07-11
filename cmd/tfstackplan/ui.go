package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
	"github.com/Fluent-Health/terraform-stack-plan/internal/ui"
)

// runUI starts the central UI service: the stateless aggregator over the tier
// serves, configured by the top-level `ui {}` block.
func runUI(args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFilename, "HCL config file")
	addr := fs.String("addr", ":8081", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan ui:", err)
		return 1
	}
	if cfg.UI == nil {
		fmt.Fprintln(os.Stderr, "tfstackplan ui: config has no ui {} block")
		return 1
	}
	uiCfg, err := buildUIConfig(ctx, cfg.UI)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan ui:", err)
		return 1
	}
	app, err := ui.New(uiCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan ui:", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "tfstackplan ui: listening on %s (%d tiers)\n", *addr, len(uiCfg.Tiers))
	srv := &http.Server{Addr: *addr, Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan ui:", err)
		return 1
	}
	return 0
}

// buildUIConfig resolves the ui {} block into runtime wiring: env-var secrets,
// the Google OAuth client + id_token verifier, and one OIDC token source per
// tier. A tier whose ambient credentials cannot mint ID tokens degrades to
// unauthenticated calls (the tier's own auth then rejects them visibly)
// rather than blocking startup — local runs against auth-disabled serves need
// no credentials at all.
func buildUIConfig(ctx context.Context, u *config.UIConfig) (ui.Config, error) {
	if u.SessionSecretEnv == "" {
		return ui.Config{}, fmt.Errorf("ui: session_secret_env is required")
	}
	sessionSecret := os.Getenv(u.SessionSecretEnv)
	if sessionSecret == "" {
		return ui.Config{}, fmt.Errorf("ui: %s is empty — set the session secret", u.SessionSecretEnv)
	}
	out := ui.Config{
		PublicBaseURL: u.PublicBaseURL,
		SessionSecret: sessionSecret,
	}
	if u.OAuth != nil {
		clientSecret := os.Getenv(u.OAuth.ClientSecretEnv)
		if clientSecret == "" {
			return ui.Config{}, fmt.Errorf("ui oauth: %s is empty — set the OAuth client secret", u.OAuth.ClientSecretEnv)
		}
		if u.PublicBaseURL == "" {
			return ui.Config{}, fmt.Errorf("ui oauth: public_base_url is required (the OAuth redirect base)")
		}
		out.OAuth = &oauth2.Config{
			ClientID:     u.OAuth.ClientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			RedirectURL:  u.PublicBaseURL + "/auth/callback",
			Scopes:       []string{"openid", "email", "profile"},
		}
		out.AllowedDomain = u.OAuth.AllowedDomain
		out.QuotaProject = u.OAuth.QuotaProject
		verify, err := gauth.ClaimsVerifier(ctx, []string{u.OAuth.ClientID})
		if err != nil {
			return ui.Config{}, fmt.Errorf("ui oauth: build id_token verifier: %w", err)
		}
		out.VerifyIDToken = verify
	}
	for _, t := range u.Tiers {
		token, err := gauth.Source(ctx, t.Audience)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tfstackplan ui: tier %s: no OIDC identity (%v) — calls will be unauthenticated\n", t.Name, err)
			token = nil
		}
		out.Tiers = append(out.Tiers, ui.Tier{Name: t.Name, URL: t.URL, Token: token})
	}
	return out, nil
}
