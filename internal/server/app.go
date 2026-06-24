// Package server is the control-plane HTTP service: it records multi-stack
// Terraform executions, drives one GitHub check run per environment, and
// projects approval-gate state — all as a pure function of its SQLite store.
// Terraform itself runs in the user's own CI; this server observes and gates.
package server

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/eventsourcing"
	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

//go:embed templates/*.gohtml
var templatesFS embed.FS

// Config is the server runtime configuration.
type Config struct {
	// WebhookSecret is the bearer token required on /api/* mutations. Empty
	// disables auth (local/dev only).
	WebhookSecret string
	// PublicBaseURL is the public origin used for check-run Details links. Empty
	// derives it from the inbound request (e.g. behind a TLS-terminating proxy).
	PublicBaseURL string
	// LogsDir is where per-stack log buffers are written. Empty disables log
	// ingestion (the endpoints become no-ops).
	LogsDir string
	// GroupDepth controls the live DAG's grouping: stacks fold into groups by
	// their first GroupDepth path segments. 0 = unset → handlers default to 2.
	GroupDepth int
	// GroupPattern is a regexp whose first capture group is the group key;
	// overrides GroupDepth. Empty → fall back to GroupDepth grouping.
	GroupPattern string
	// PushServiceAccount is the allowed verified OIDC email for /pubsub/push.
	// Empty accepts any verified token.
	PushServiceAccount string
	// GitHubWebhookSecret is the HMAC-SHA256 secret GitHub sends on every webhook
	// delivery (X-Hub-Signature-256). Empty disables the /github/webhook endpoint.
	GitHubWebhookSecret string
	// Progress is the optional per-operation weighted phase set (from the policy
	// file's progress{} block) driving the full-progress bar. nil → built-in
	// fractions.
	Progress *config.ProgressConfig
}

// App is the HTTP application.
type App struct {
	db  *sql.DB
	gh  GitHub
	cfg Config
	hub *hub
	// Approval is the optional approval-gate backend. nil disables gating
	// (gates recorded AWAITING are never satisfied → action_required). Set after
	// construction (e.g. by the serve command), so New's signature is unchanged.
	Approval approval.Backend
	// Objects is the optional object store for completed-log offload. nil keeps
	// logs in the local buffer only. Set after construction, like Approval.
	Objects ObjectStore
	// PushVerifier verifies a Pub/Sub push OIDC bearer token, returning the
	// token's email claim. Set externally (like Approval/Objects); nil disables
	// the /pubsub/push endpoint (it returns 404).
	PushVerifier func(ctx context.Context, bearer string) (email string, err error)
	// tmpl holds the page templates (parsed once from the embedded FS in New).
	tmpl *template.Template
	// groupRE is the compiled Config.GroupPattern (nil → depth grouping).
	groupRE *regexp.Regexp
	// now returns the current time. Injectable so claim-lease and janitor time
	// is deterministic in tests; defaults to time.Now in New.
	now func() time.Time
	// shell is the reconcile engine — the sole path for gate/execution state.
	shell *Shell
	// eventStore is the append-only gate event log + snapshot cache. The shell
	// appends Decide's events here and replays them on gather; gate_targets is a
	// derived projection.
	eventStore *store.EventStore
	// gateDecider is the generic eventsourcing host wired to the gate aggregate.
	gateDecider eventsourcing.Decider[reconcile.ChangeSet, reconcile.Event]
	// claimsDecider is the generic eventsourcing host wired to the apply-lock
	// claim ledger (the env:<env> stream). apply_claims is a derived projection.
	claimsDecider eventsourcing.Decider[claims.ClaimSet, claims.Event]
}

// New builds an App.
func New(db *sql.DB, gh GitHub, cfg Config) *App {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"statusBadge": statusBadge,
	}).ParseFS(templatesFS, "templates/*.gohtml"))
	var groupRE *regexp.Regexp
	if cfg.GroupPattern != "" {
		if re, err := regexp.Compile(cfg.GroupPattern); err == nil {
			groupRE = re
		} else {
			log.Printf("server: invalid group pattern %q: %v (falling back to depth grouping)", cfg.GroupPattern, err)
		}
	}
	a := &App{db: db, gh: gh, cfg: cfg, hub: newHub(), tmpl: tmpl, groupRE: groupRE, now: time.Now}
	a.eventStore = store.NewEventStore(db)
	a.gateDecider = eventsourcing.Decider[reconcile.ChangeSet, reconcile.Event]{
		Initial:           func() reconcile.ChangeSet { return reconcile.ChangeSet{Gate: reconcile.NotClassified{}} },
		Evolve:            reconcile.Evolve,
		MarshalEvent:      reconcile.MarshalEvent,
		UnmarshalEvent:    reconcile.UnmarshalEvent,
		MarshalSnapshot:   reconcile.MarshalSnapshot,
		UnmarshalSnapshot: reconcile.UnmarshalSnapshot,
	}
	a.claimsDecider = eventsourcing.Decider[claims.ClaimSet, claims.Event]{
		Initial:           claims.Empty,
		Evolve:            claims.Evolve,
		MarshalEvent:      claims.MarshalEvent,
		UnmarshalEvent:    claims.UnmarshalEvent,
		MarshalSnapshot:   claims.MarshalSnapshot,
		UnmarshalSnapshot: claims.UnmarshalSnapshot,
	}
	a.shell = NewShell(a)
	return a
}

// Routes returns the HTTP handler. Uses stdlib method-pattern routing (Go 1.22+).
// Auth model:
//   - /api/*            require a short-lived HS256 JWT (aud=api) — generated by CI
//   - /live/* /pr/ /{$} require a 30-day HS256 JWT (aud=view) via ?token= or cookie
//   - /img/* /assets/*  public (GitHub camo + CSS must not require auth)
//   - /logs/*           public (accessed inline by the live page after view auth)
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /{$}", a.viewAuth(http.HandlerFunc(a.handleIndex)))
	mux.Handle("GET /pr/{n}", a.viewAuth(http.HandlerFunc(a.handlePRTimeline)))
	mux.HandleFunc("GET /img/{name}", a.handleImg)
	mux.HandleFunc("GET /assets/{file}", a.handleAsset)
	mux.Handle("GET /live/{id}", a.viewAuth(http.HandlerFunc(a.handleLive)))
	mux.Handle("GET /live/{id}/events", a.viewAuth(http.HandlerFunc(a.handleLiveEvents)))
	mux.HandleFunc("GET /logs/{exec}/{stack...}", a.handleLogServe)
	mux.HandleFunc("GET /plan/{exec}/{stack...}", a.handlePlanServe)
	mux.HandleFunc("POST /pubsub/push", a.handlePushEvent)
	mux.HandleFunc("POST /github/webhook", a.handleGitHubWebhook)
	mux.Handle("POST /api/init", a.auth(http.HandlerFunc(a.handleInit)))
	mux.Handle("POST /api/phase", a.auth(http.HandlerFunc(a.handlePhase)))
	mux.Handle("POST /api/update", a.auth(http.HandlerFunc(a.handleUpdate)))
	mux.Handle("POST /api/finalize", a.auth(http.HandlerFunc(a.handleFinalize)))
	mux.Handle("POST /api/gate/check", a.auth(http.HandlerFunc(a.handleGateCheck)))
	mux.Handle("POST /api/gate/revoke", a.auth(http.HandlerFunc(a.handleGateRevoke)))
	mux.Handle("POST /api/logs", a.auth(http.HandlerFunc(a.handleLogs)))
	mux.Handle("POST /api/claims/list", a.auth(http.HandlerFunc(a.handleClaimsList)))
	mux.Handle("POST /api/claims/release", a.auth(http.HandlerFunc(a.handleClaimsRelease)))
	return mux
}

// auth enforces a short-lived HS256 JWT (aud=api) on /api/* mutations.
// An empty configured secret disables the check (local/dev).
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.WebhookSecret != "" {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, prefix) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if _, err := jwtutil.Validate(strings.TrimPrefix(h, prefix), a.cfg.WebhookSecret, "api"); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// viewAuth enforces a 30-day HS256 JWT (aud=view) on GET routes.
// Accepts ?token=<jwt> (first access; sets a session cookie so sub-resource
// requests from the same browser carry auth without the token in every URL) or
// the view_session cookie set on a prior ?token= access.
// An empty configured secret disables the check (local/dev).
func (a *App) viewAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.WebhookSecret != "" {
			token := ""
			if c, err := r.Cookie("view_session"); err == nil {
				token = c.Value
			}
			if q := r.URL.Query().Get("token"); q != "" {
				token = q
			}
			if _, err := jwtutil.Validate(token, a.cfg.WebhookSecret, "view"); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("token") != "" {
				http.SetCookie(w, &http.Cookie{
					Name:     "view_session",
					Value:    token,
					Path:     "/",
					MaxAge:   30 * 24 * 3600,
					HttpOnly: true,
					Secure:   true,
					SameSite: http.SameSiteLaxMode,
				})
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

// liveURL builds the per-execution live-page URL. When WebhookSecret is set,
// appends a 30-day view JWT so GitHub check-run recipients can open the page
// without additional auth.
func (a *App) liveURL(base, id string) string {
	u := fmt.Sprintf("%s/live/%s", base, id)
	if a.cfg.WebhookSecret != "" {
		if tok, err := jwtutil.Make(a.cfg.WebhookSecret, "viewer", "view", 30*24*time.Hour); err == nil {
			u += "?token=" + url.QueryEscape(tok)
		}
	}
	return u
}
