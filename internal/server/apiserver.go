package server

import (
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
)

// apiServer adapts App's /api handlers to the generated api.ServerInterface.
// Routing, path-parameter binding, and the per-operation scope injection come
// from the OpenAPI contract (api/openapi.yaml); the handler bodies — and
// therefore the wire behavior pinned by testdata/wire — are unchanged.
type apiServer struct {
	app *App
}

var _ api.ServerInterface = apiServer{}

func (s apiServer) InitExecution(w http.ResponseWriter, r *http.Request) { s.app.handleInit(w, r) }
func (s apiServer) ReportPhase(w http.ResponseWriter, r *http.Request)   { s.app.handlePhase(w, r) }
func (s apiServer) UpdateStack(w http.ResponseWriter, r *http.Request)   { s.app.handleUpdate(w, r) }
func (s apiServer) FinalizeExecution(w http.ResponseWriter, r *http.Request) {
	s.app.handleFinalize(w, r)
}
func (s apiServer) AppendLogs(w http.ResponseWriter, r *http.Request) { s.app.handleLogs(w, r) }
func (s apiServer) CheckGate(w http.ResponseWriter, r *http.Request)  { s.app.handleGateCheck(w, r) }
func (s apiServer) RevokeGate(w http.ResponseWriter, r *http.Request) { s.app.handleGateRevoke(w, r) }
func (s apiServer) ListClaims(w http.ResponseWriter, r *http.Request) { s.app.handleClaimsList(w, r) }
func (s apiServer) ReleaseClaims(w http.ResponseWriter, r *http.Request) {
	s.app.handleClaimsRelease(w, r)
}
func (s apiServer) GetExecution(w http.ResponseWriter, r *http.Request, id string) {
	s.app.handleGetExecution(w, r, id)
}
func (s apiServer) ListExecutions(w http.ResponseWriter, r *http.Request, params api.ListExecutionsParams) {
	s.app.handleListExecutions(w, r, params)
}
func (s apiServer) ListApprovals(w http.ResponseWriter, r *http.Request) {
	s.app.handleListApprovals(w, r)
}
func (s apiServer) GetPR(w http.ResponseWriter, r *http.Request, n int) {
	s.app.handleGetPR(w, r, n)
}

func (s apiServer) InspectClaims(w http.ResponseWriter, r *http.Request, env string) {
	s.app.handleInspectClaims(w, r, env)
}

func (s apiServer) InspectEvents(w http.ResponseWriter, r *http.Request, stream string, params api.InspectEventsParams) {
	s.app.handleInspectEvents(w, r, stream, params)
}

func (s apiServer) InspectGate(w http.ResponseWriter, r *http.Request, pr int, env string) {
	s.app.handleInspectGate(w, r, pr, env)
}

func (s apiServer) InspectGrants(w http.ResponseWriter, r *http.Request, params api.InspectGrantsParams) {
	s.app.handleInspectGrants(w, r, params)
}

func (s apiServer) InspectOverview(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// apiAuth is the OIDC auth middleware for the generated router. The accepted
// scopes for the matched operation ride the request context (api.OidcScopes,
// injected by the generated wrapper from the spec's security requirements), so
// the contract is the single source of truth for who may call what.
func (a *App) apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scopes, _ := r.Context().Value(api.OidcScopes).([]string)
		a.auth(next, scopes...).ServeHTTP(w, r)
	})
}
