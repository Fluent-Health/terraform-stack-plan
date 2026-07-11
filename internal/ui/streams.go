package ui

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
)

// Streaming/HTML proxies — session-authed but outside the OpenAPI contract,
// exactly like their tier-side counterparts (OpenAPI models neither SSE nor
// HTML fragments usefully):
//
//	GET /api/tiers/{tier}/executions/{id}/events    ← tier /api/execution/{id}/events (SSE)
//	GET /api/tiers/{tier}/logs/{exec}/{stack...}    ← tier /logs/{exec}/{stack...}   (text or SSE with ?follow=1)
//	GET /api/tiers/{tier}/plan/{exec}/{stack...}    ← tier /plan/{exec}/{stack...}   (rendered HTML fragment)
//
// The proxy attaches the tier's OIDC bearer (the events stream requires it
// today; logs/plan will once the tier HTML retirement locks them down),
// forwards the query string and Last-Event-ID (log resume), and streams with
// a per-write flush. The browser keeps talking to one authenticated origin.

// streamClient has no overall timeout: SSE responses are deliberately
// long-lived. Lifetime is bound to the browser's request context instead —
// when the EventSource closes, the proxied request is cancelled.
var streamClient = &http.Client{}

func (a *App) registerStreamRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/tiers/{tier}/executions/{id}/events",
		a.sessionAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.proxyStream(w, r, r.PathValue("tier"), "/api/execution/"+url.PathEscape(r.PathValue("id"))+"/events")
		})))
	mux.Handle("GET /api/tiers/{tier}/logs/{exec}/{stack...}",
		a.sessionAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.proxyStream(w, r, r.PathValue("tier"), "/logs/"+url.PathEscape(r.PathValue("exec"))+"/"+r.PathValue("stack"))
		})))
	mux.Handle("GET /api/tiers/{tier}/plan/{exec}/{stack...}",
		a.sessionAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.proxyStream(w, r, r.PathValue("tier"), "/plan/"+url.PathEscape(r.PathValue("exec"))+"/"+r.PathValue("stack"))
		})))
}

// proxyStream relays one tier response, flushing as bytes arrive so SSE
// events reach the browser immediately. Non-streaming responses (plan
// fragments, static log reads) pass through the same way.
func (a *App) proxyStream(w http.ResponseWriter, r *http.Request, tierName, path string) {
	var tier *Tier
	for i := range a.tiers {
		if a.tiers[i].Name == tierName {
			tier = &a.tiers[i]
			break
		}
	}
	if tier == nil {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	target := tier.URL + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if tier.Token != nil {
		bearer, err := tier.Token(r.Context())
		if err != nil {
			log.Printf("ui: tier %s: mint stream token: %v", tierName, err)
			http.Error(w, fmt.Sprintf("tier %s unreachable", tierName), http.StatusBadGateway)
			return
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("tier %s unreachable", tierName), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		// Push the headers out now: an idle SSE stream may send no bytes for
		// a long time, and EventSource needs the response line to consider
		// itself connected.
		flusher.Flush()
	}
	buf := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // browser went away
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("ui: stream from tier %s ended: %v", tierName, err)
			}
			return
		}
	}
}
