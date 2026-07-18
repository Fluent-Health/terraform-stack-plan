// Package server is the control-plane HTTP service: it records multi-stack
// Terraform executions, drives one GitHub check run per environment, and
// projects approval-gate state — all as a pure function of its SQLite store.
// Terraform itself runs in the user's own CI; this server observes and gates.
package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/claims"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/eventsourcing"
	"github.com/Fluent-Health/terraform-stack-plan/internal/execution"
	"github.com/Fluent-Health/terraform-stack-plan/internal/executor"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// Config is the server runtime configuration.
type Config struct {
	// PublicBaseURL is the public origin of THIS serve, used to build check-run
	// Details links and as the OIDC audience for its own endpoints. Empty
	// derives it from the inbound request.
	PublicBaseURL string
	// UIBaseURL is the central UI service's external base URL. Check-run
	// details and approval links point there; empty leaves them unset. The
	// UI's configured tier names must match serve environments (its routes
	// are /t/<tier>/e/<id> and /pr/<n>).
	UIBaseURL string
	// Environment is this serve's tier (e.g. "nonprod"). Run triggering scopes
	// its ChangeSets to it; empty disables webhook-driven run triggering.
	Environment string
	// LogsDir is where per-stack log buffers are written. Empty disables log
	// ingestion (the endpoints become no-ops).
	LogsDir string
	// PushServiceAccount is the allowed verified OIDC email for /pubsub/push.
	// Empty accepts any verified token.
	PushServiceAccount string
	// APIPrincipals maps a verified OIDC caller email (lowercase) to the API
	// scopes it holds (see the scope* constants). Only consulted when
	// App.APIVerifier is set.
	APIPrincipals map[string][]string
	// GitHubWebhookSecret is the HMAC-SHA256 secret GitHub sends on every webhook
	// delivery (X-Hub-Signature-256). Empty disables the /github/webhook endpoint.
	GitHubWebhookSecret string
	// Progress is the optional per-operation weighted phase set (from the policy
	// file's progress{} block) driving the full-progress bar. nil → built-in
	// fractions.
	Progress *config.ProgressConfig
	// BuildTriggerNames maps a Cloud Build trigger NAME to the run kind
	// ("plan"/"apply") it drives. Populated from the executor block; lets the
	// /pubsub/cloud-builds ingest recognize builds from serve's own triggers and
	// derive their kind. Empty disables inbound build reconciliation.
	BuildTriggerNames map[string]string
	// RequesterPool is the set of service-account identities configured for the applier pool.
	RequesterPool []string
}

// App is the HTTP application.
type App struct {
	db  *sql.DB
	gh  GitHub
	cfg Config
	hub *hub
	// Approval is the optional approval-gate backend. nil disables gating
	// (gates recorded AWAITING are never satisfied — the check stays pending
	// with the awaiting-approval title). Set after
	// construction (e.g. by the serve command), so New's signature is unchanged.
	Approval approval.Backend
	// Objects is the optional object store for completed-log offload. nil keeps
	// logs in the local buffer only. Set after construction, like Approval.
	Objects ObjectStore
	// Executor is the optional CI backend serve drives when it triggers runs
	// itself (webhook → build). nil keeps serve reactive-only (runs start via
	// the CI system's own triggers, as before). Set after construction.
	Executor executor.Backend
	// PushVerifier verifies a Pub/Sub push OIDC bearer token, returning the
	// token's email claim. Set externally (like Approval/Objects); nil disables
	// the /pubsub/push endpoint (it returns 404).
	PushVerifier func(ctx context.Context, bearer string) (email string, err error)
	// APIVerifier verifies a Google-signed OIDC bearer token on /api/* routes,
	// returning the token's email claim (audience checking included). Set
	// externally, like PushVerifier; nil disables /api/* auth entirely
	// (local/dev). It is the only /api/* credential — the legacy shared-secret
	// HS256 path was removed once every caller migrated to OIDC.
	APIVerifier func(ctx context.Context, bearer string) (email string, err error)
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
	// execDecider is the generic eventsourcing host wired to the per-execution
	// lifecycle aggregate (stream "run:<execID>"). Inert until A2 rewires ingest.
	execDecider eventsourcing.Decider[execution.State, execution.Event]
}

// New builds an App.
func New(db *sql.DB, gh GitHub, cfg Config) *App {
	a := &App{db: db, gh: gh, cfg: cfg, hub: newHub(), now: time.Now}
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
	a.execDecider = eventsourcing.Decider[execution.State, execution.Event]{
		Initial:           execution.Empty,
		Evolve:            execution.Evolve,
		MarshalEvent:      execution.MarshalEvent,
		UnmarshalEvent:    execution.UnmarshalEvent,
		MarshalSnapshot:   execution.MarshalSnapshot,
		UnmarshalSnapshot: execution.UnmarshalSnapshot,
	}
	a.shell = NewShell(a)
	return a
}

// Routes returns the HTTP handler. Uses stdlib method-pattern routing (Go 1.22+).
// Auth model:
//   - /api/* /logs/* /plan/*  require a Google-signed OIDC ID token
//     (aud=serve URL); the central UI proxies them for browsers
//
// The tier serves no longer serve HTML pages — the central UI (`tfstackplan
// ui`) is the human surface; the 30-day view-JWT machinery retired with it.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /logs/{exec}/{stack...}", a.auth(http.HandlerFunc(a.handleLogServe), scopeReport, scopeRead, scopeAdmin))
	mux.Handle("GET /plan/{exec}/{stack...}", a.auth(http.HandlerFunc(a.handlePlanServe), scopeReport, scopeRead, scopeAdmin))
	mux.HandleFunc("POST /pubsub/push", a.handlePushEvent)
	mux.HandleFunc("POST /pubsub/cloud-builds", a.handleCloudBuildPush)
	mux.HandleFunc("POST /github/webhook", a.handleGitHubWebhook)
	// The JSON /api surface is routed by the generated OpenAPI router —
	// api/openapi.yaml is the contract, and each operation's accepted scopes
	// ride the request context from its security requirements. The SSE stream
	// is outside the contract and stays hand-registered.
	api.HandlerWithOptions(apiServer{app: a}, api.StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []api.MiddlewareFunc{a.apiAuth},
	})
	mux.Handle("GET /api/execution/{id}/events", a.auth(http.HandlerFunc(a.handleGetExecutionEvents), scopeReport, scopeRead, scopeAdmin))
	return mux
}

// API scopes, granted per verified OIDC identity via Config.APIPrincipals.
// Each /api/* route lists the scopes that may call it (any-of). The vocabulary
// lives in config (validated at load); these are local aliases. Note: claim
// release is not ownership-checked — the runner releases claims for whichever
// PR it applies, an association the server cannot verify; the verified actor
// is what gets audited.
const (
	scopeReport = config.ScopeReport // CI runner: execution lifecycle events, logs, gates, claims
	scopeRead   = config.ScopeRead   // read-only: execution state/events, claims listing
	scopeAdmin  = config.ScopeAdmin  // operator surgery: claim release (and future /api/admin/* verbs)
)

// actorKey carries the verified /api/* caller identity in the request context.
type actorKey struct{}

// Actor returns the authenticated caller of an /api/* request: the verified
// OIDC email, or "" when auth is disabled (no APIVerifier).
func Actor(r *http.Request) string {
	v, _ := r.Context().Value(actorKey{}).(string)
	return v
}

// auth enforces bearer auth on /api/* routes: a Google-signed OIDC ID token
// (verified by APIVerifier) whose verified email must hold one of the route's
// scopes in Config.APIPrincipals. With no verifier configured the check is
// disabled (local/dev). The pre-OIDC shared-secret HS256 scheme was removed
// once every caller had migrated to OIDC.
func (a *App) auth(next http.Handler, scopes ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.APIVerifier == nil {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(h, prefix)
		email, err := a.APIVerifier(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		email = strings.ToLower(email)
		if !hasAnyScope(a.cfg.APIPrincipals[email], scopes) {
			log.Printf("server: api auth: %s lacks scope %v for %s %s", email, scopes, r.Method, r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, email)))
	})
}

// hasAnyScope reports whether the granted scope set holds any required scope.
func hasAnyScope(granted, required []string) bool {
	return slices.ContainsFunc(granted, func(g string) bool { return slices.Contains(required, g) })
}

// statusContext is the per-environment context the server drives for the plan
// gate: "plan/<environment>" (or "plan" when environment is empty).
func statusContext(environment string) string {
	if environment == "" {
		return "plan"
	}
	return "plan/" + environment
}

// planCheckName is the GitHub check-run NAME for the plan gate. Armed
// (serve-as-driver) tiers post the consolidated terraform/<env> context; the
// stored gate identity (statusContext) stays plan/<env> either way.
func (a *App) planCheckName(environment string) string {
	if a.runTriggerArmed() {
		return consolidatedCheckName(environment)
	}
	return checkRunName(environment)
}

// mergeGateCheckName is the check name for merge-lock-only surfaces (merge
// group heads, legacy PR heads): folded into terraform/<env> when armed.
func (a *App) mergeGateCheckName(environment string) string {
	if a.runTriggerArmed() {
		return consolidatedCheckName(environment)
	}
	return applyLockName(environment)
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

// supersedeExecution marks old superseded by new and redirects its live page
// (the SSE consumers parse the "superseded:<id>" message). One helper for both
// supersede writers — the runner-init path and the run-trigger cancel path. The
// column write itself routes through the execution aggregate (ReportSupersede),
// so superseded_by is event-sourced and survives stream replay.
func (a *App) supersedeExecution(ctx context.Context, oldID, newID string) {
	if err := a.shell.HandleExec(ctx, oldID, execution.ReportSupersede{By: newID}); err != nil {
		log.Printf("supersede %s -> %s: %v", oldID, newID, err)
		return
	}
	if a.hub != nil {
		a.hub.publish("exec:"+oldID, "superseded:"+newID)
	}
	go a.closeSupersededCheckRun(context.Background(), oldID, newID)
}

// closeSupersededCheckRun terminally updates a check-run on GitHub to completed/neutral
// if it was marked as in_progress, so it doesn't get stranded forever.
func (a *App) closeSupersededCheckRun(ctx context.Context, id, supersededBy string) {
	e, err := store.GetExecution(a.db, id)
	if err != nil || !e.CheckRunID.Valid || e.CheckRunID.Int64 == 0 {
		return
	}
	upd := CheckRunUpdate{
		Title:      "Superseded",
		Summary:    fmt.Sprintf("This execution was superseded by a newer run: %s", supersededBy),
		Conclusion: "neutral",
	}
	if err := a.gh.UpdateCheckRun(ctx, e.Repo, e.CheckRunID.Int64, upd); err != nil {
		log.Printf("close superseded check run %s: %v", id, err)
	}
}

// uiURL builds the central UI's execution-view URL for check-run Details
// links ("" when no UI is configured — GitHub omits the link; the check-run
// body still carries everything). The UI routes by tier name, which matches
// this serve's environment by convention.
func (a *App) uiURL(pr int, id string) string {
	if a.cfg.UIBaseURL == "" {
		return ""
	}
	if pr > 0 {
		return fmt.Sprintf("%s/pr/%d", a.cfg.UIBaseURL, pr)
	}
	return fmt.Sprintf("%s/t/%s/e/%s", a.cfg.UIBaseURL, a.cfg.Environment, id)
}
