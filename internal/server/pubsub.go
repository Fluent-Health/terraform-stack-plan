package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// pushEnvelope is the Google Pub/Sub push delivery shape.
type pushEnvelope struct {
	Message struct {
		Data       string            `json:"data"` // base64
		Attributes map[string]string `json:"attributes"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// handlePushEvent ingests a Pub/Sub push (OIDC-verified) and reconciles on demand:
// a latency win over the poll loop. Public — the OIDC token is the auth. Disabled
// (404) when no PushVerifier is configured.
func (a *App) handlePushEvent(w http.ResponseWriter, r *http.Request) {
	if a.PushVerifier == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" || tok == r.Header.Get("Authorization") {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	email, err := a.PushVerifier(r.Context(), tok)
	if err != nil {
		http.Error(w, "invalid OIDC token", http.StatusUnauthorized)
		return
	}
	if a.cfg.PushServiceAccount != "" && email != a.cfg.PushServiceAccount {
		http.Error(w, "unauthorized push identity", http.StatusForbidden)
		return
	}
	var env pushEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		http.Error(w, "bad push envelope", http.StatusBadRequest)
		return
	}
	// Optional targeting: message data is base64 JSON, e.g. {"id":"<exec>"}.
	id := env.Message.Attributes["execution_id"]
	if id == "" && env.Message.Data != "" {
		if raw, derr := base64.StdEncoding.DecodeString(env.Message.Data); derr == nil {
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &p)
			id = p.ID
		}
	}
	if id != "" {
		a.drive(r.Context(), id, strings.TrimRight(a.cfg.PublicBaseURL, "/"), true)
	} else {
		a.reconcilePending(r.Context())
	}
	w.WriteHeader(http.StatusNoContent) // ack
}
