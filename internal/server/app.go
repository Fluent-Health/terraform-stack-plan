// Package server is the control-plane HTTP service: it records multi-stack
// Terraform executions, drives one GitHub check run per environment, and
// projects approval-gate state — all as a pure function of its SQLite store.
// Terraform itself runs in the user's own CI; this server observes and gates.
package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

// Config is the server runtime configuration.
type Config struct {
	// WebhookSecret is the bearer token required on /api/* mutations. Empty
	// disables auth (local/dev only).
	WebhookSecret string
	// PublicBaseURL is the public origin used for check-run Details links. Empty
	// derives it from the inbound request (e.g. behind a TLS-terminating proxy).
	PublicBaseURL string
	// UseChecks true → drive a rich GitHub check run (needs checks:write);
	// false → link mode, posting a commit status (needs statuses:write).
	UseChecks bool
}

// App is the HTTP application.
type App struct {
	db  *sql.DB
	gh  GitHub
	cfg Config
	// Approval is the optional approval-gate backend. nil disables gating
	// (gates recorded AWAITING are never satisfied → action_required). Set after
	// construction (e.g. by the serve command), so New's signature is unchanged.
	Approval approval.Backend
}

// New builds an App.
func New(db *sql.DB, gh GitHub, cfg Config) *App {
	return &App{db: db, gh: gh, cfg: cfg}
}

// Routes returns the HTTP handler: a public health check plus bearer-authed
// mutations. Uses stdlib method-pattern routing (Go 1.22+).
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /img/{name}", a.handleImg)
	mux.HandleFunc("GET /live/{id}", a.handleLive)
	mux.Handle("POST /api/init", a.auth(http.HandlerFunc(a.handleInit)))
	mux.Handle("POST /api/phase", a.auth(http.HandlerFunc(a.handlePhase)))
	mux.Handle("POST /api/update", a.auth(http.HandlerFunc(a.handleUpdate)))
	mux.Handle("POST /api/finalize", a.auth(http.HandlerFunc(a.handleFinalize)))
	return mux
}

// auth enforces a bearer token on mutations. An empty configured secret disables
// the check (local/dev).
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.WebhookSecret != "" {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, prefix) || strings.TrimPrefix(h, prefix) != a.cfg.WebhookSecret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// statusContext is the per-environment context the server drives for the plan
// gate: "plan/<environment>" (or "plan" when environment is empty).
func statusContext(environment string) string {
	if environment == "" {
		return "plan"
	}
	return "plan/" + environment
}

// isGate reports whether a run's declared context drives the plan gate (versus a
// non-gate context such as a post-merge apply). Empty context = the gate.
func isGate(context, environment string) bool {
	return context == "" || context == statusContext(environment)
}

// baseURL is the public origin for building Details links. Prefers the
// configured PublicBaseURL; otherwise derives it from the request.
func (a *App) baseURL(r *http.Request) string {
	if a.cfg.PublicBaseURL != "" {
		return strings.TrimRight(a.cfg.PublicBaseURL, "/")
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

func badRequest(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// liveURL builds the per-execution live-page URL.
func liveURL(base, id string) string { return fmt.Sprintf("%s/live/%s", base, id) }
