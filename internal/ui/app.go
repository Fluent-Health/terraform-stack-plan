// Package ui is the central UI service: a single pane of glass over the tier
// serves. It is a stateless aggregator — no domain state of its own — with
// Google OAuth login for humans (encrypted session cookie; the SPA never sees
// tokens) and Google OIDC service identity toward the tier serves. Routing and
// shapes for its JSON API come from api/ui.openapi.yaml (internal/uiapi).
package ui

import (
	"context"
	"log"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
	"github.com/Fluent-Health/terraform-stack-plan/internal/uiapi"
)

// Tier is one tier serve the UI aggregates. Token mints the Google OIDC ID
// tokens the tier's api_auth accepts (nil = unauthenticated — local/dev
// against an auth-disabled serve).
type Tier struct {
	Name  string
	URL   string
	Token gauth.TokenFunc
}

// Config carries the wiring the ui face resolves from the `ui {}` block and
// its env-var secrets.
type Config struct {
	PublicBaseURL string // external base; the OAuth redirect URI is <base>/auth/callback
	SessionSecret string
	AllowedDomain string // required id_token hd claim, lowercase
	// OAuth is the Google authorization-code client. Endpoint is overridable
	// so tests run against a fake token server.
	OAuth *oauth2.Config
	// QuotaProject rides x-goog-user-project on user-token PAM calls (user
	// credentials attribute API quota to the OAuth client's project; empty
	// sends no header). PAMBaseURL overrides the PAM endpoint for tests.
	QuotaProject string
	PAMBaseURL   string
	// VerifyIDToken validates the id_token from the code exchange and returns
	// its claims (signature + audience checked; claim semantics enforced by
	// the login handler). Injectable for offline tests.
	VerifyIDToken gauth.VerifyClaimsFunc
	Tiers         []Tier
}

// App is the central UI HTTP service.
type App struct {
	cfg     Config
	codec   *sessionCodec
	tiers   []Tier
	clients map[string]*api.Client
}

// New builds the App. The session secret is required — every surface except
// /healthz sits behind the login.
func New(cfg Config) (*App, error) {
	codec, err := newSessionCodec(cfg.SessionSecret)
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, codec: codec, tiers: cfg.Tiers, clients: map[string]*api.Client{}}
	for _, t := range cfg.Tiers {
		opts := []api.ClientOption{api.WithHTTPClient(&http.Client{Timeout: 15 * time.Second})}
		if t.Token != nil {
			tok := t.Token
			opts = append(opts, api.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
				bearer, err := tok(ctx)
				if err != nil {
					return err
				}
				req.Header.Set("Authorization", "Bearer "+bearer)
				return nil
			}))
		}
		c, err := api.NewClient(t.URL, opts...)
		if err != nil {
			return nil, err
		}
		a.clients[t.Name] = c
	}
	return a, nil
}

// Routes assembles the full HTTP surface: public health, the OAuth browser
// flow, the session-authed JSON API (generated router), and the SPA fallback.
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /auth/login", a.handleLogin)
	mux.HandleFunc("GET /auth/callback", a.handleCallback)
	mux.HandleFunc("POST /auth/logout", a.handleLogout)
	mux.Handle("GET /auth/approve", a.sessionAuth(http.HandlerFunc(a.handleApproveStart)))
	mux.Handle("GET /auth/approve/callback", a.sessionAuth(http.HandlerFunc(a.handleApproveCallback)))
	uiapi.HandlerWithOptions(uiServer{app: a}, uiapi.StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []uiapi.MiddlewareFunc{a.sessionAuth},
	})
	a.registerStreamRoutes(mux)
	mux.Handle("GET /", a.spaHandler())
	return mux
}

// sessionAuth guards the JSON API: a valid session cookie or 401. The session
// rides the request context for handlers (Me).
func (a *App) sessionAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s, err := a.codec.open(c.Value)
		if err != nil {
			log.Printf("ui: session rejected: %v", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), s)))
	})
}

type sessionKey struct{}

func withSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFrom returns the verified session riding the request context (zero
// value outside sessionAuth-guarded routes).
func SessionFrom(ctx context.Context) Session {
	s, _ := ctx.Value(sessionKey{}).(Session)
	return s
}
