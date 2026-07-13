package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/api"
	"github.com/Fluent-Health/terraform-stack-plan/internal/uiapi"
)

// uiServer implements the generated uiapi.ServerInterface. Tier reads are
// typed-request proxies: the generated tier client builds the request (so the
// tier contract governs paths/params), and the response body is copied
// through verbatim — no decode/re-encode drift.
type uiServer struct {
	app *App
}

var _ uiapi.ServerInterface = uiServer{}

func (s uiServer) GetMe(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiapi.Me{Email: sess.Email})
}

func (s uiServer) ListTiers(w http.ResponseWriter, _ *http.Request) {
	out := make([]uiapi.TierInfo, 0, len(s.app.tiers))
	for _, t := range s.app.tiers {
		out = append(out, uiapi.TierInfo{Name: t.Name, Url: t.URL})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s uiServer) ListTierExecutions(w http.ResponseWriter, r *http.Request, tier uiapi.Tier, params uiapi.ListTierExecutionsParams) {
	c, ok := s.app.clients[string(tier)]
	if !ok {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	resp, err := c.ListExecutions(r.Context(), &api.ListExecutionsParams{Pr: params.Pr, Limit: params.Limit})
	s.relay(w, string(tier), resp, err)
}

func (s uiServer) ListTierApprovals(w http.ResponseWriter, r *http.Request, tier uiapi.Tier) {
	c, ok := s.app.clients[string(tier)]
	if !ok {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	resp, err := c.ListApprovals(r.Context())
	s.relay(w, string(tier), resp, err)
}

func (s uiServer) GetTierPR(w http.ResponseWriter, r *http.Request, tier uiapi.Tier, n int) {
	c, ok := s.app.clients[string(tier)]
	if !ok {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	resp, err := c.GetPR(r.Context(), n)
	s.relay(w, string(tier), resp, err)
}

func (s uiServer) GetTierExecution(w http.ResponseWriter, r *http.Request, tier uiapi.Tier, id string) {
	c, ok := s.app.clients[string(tier)]
	if !ok {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	resp, err := c.GetExecution(r.Context(), id)
	s.relay(w, string(tier), resp, err)
}

func (s uiServer) GetTierPool(w http.ResponseWriter, r *http.Request, tier uiapi.Tier) {
	c, ok := s.app.clients[string(tier)]
	if !ok {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	resp, err := c.InspectPool(r.Context())
	s.relay(w, string(tier), resp, err)
}

// relay copies a tier response through: status, Content-Type, body. A
// transport failure becomes 502 naming the tier — one tier down must not
// look like a UI failure, and must not affect the others.
func (s uiServer) relay(w http.ResponseWriter, tier string, resp *http.Response, err error) {
	if err != nil {
		http.Error(w, fmt.Sprintf("tier %s unreachable", tier), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
