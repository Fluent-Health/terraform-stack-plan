package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval/gcppam"
)

// In-UI PAM approve/deny — the incremental-consent popup flow. The whole
// exchange is STATELESS by construction: the approval intent (tier, grant,
// decision, reason) rides the OAuth `state` parameter AEAD-sealed and bound
// to the session's email, the callback exchanges the code, spends the user's
// short-lived access token on the single PAM call, and discards it. No user
// credential is ever stored; GCP enforces the real authorization (no PAM
// approver IAM → 403, surfaced verbatim) and the PAM audit log records the
// human. First use asks for `cloud-platform` consent (PAM exposes no narrower
// scope — the token is still bounded by the user's own IAM); afterwards the
// popup is a silent redirect (include_granted_scopes).

// cloudPlatformScope is the scope PAM requires; there is no narrower one.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// approveState is the sealed OAuth state payload.
type approveState struct {
	Email   string    `json:"email"` // must match the callback's session
	Tier    string    `json:"tier"`
	Grant   string    `json:"grant"`
	Approve bool      `json:"approve"`
	Reason  string    `json:"reason"`
	Expires time.Time `json:"expires"`
}

// consentOAuth is the login client re-targeted at the consent callback with
// the cloud-platform scope.
func (a *App) consentOAuth() *oauth2.Config {
	c := *a.cfg.OAuth
	c.RedirectURL = a.cfg.PublicBaseURL + "/auth/approve/callback"
	c.Scopes = []string{cloudPlatformScope}
	return &c
}

// handleApproveStart begins the popup flow:
// GET /auth/approve?tier=..&grant=..&decision=approve|deny&reason=..
func (a *App) handleApproveStart(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OAuth == nil || a.cfg.PublicBaseURL == "" {
		http.Error(w, "approvals are not configured (oauth + public_base_url required)", http.StatusNotImplemented)
		return
	}
	q := r.URL.Query()
	tier, grant, decision, reason := q.Get("tier"), q.Get("grant"), q.Get("decision"), q.Get("reason")
	if _, ok := a.clients[tier]; !ok {
		http.Error(w, "unknown tier", http.StatusNotFound)
		return
	}
	if decision != "approve" && decision != "deny" {
		http.Error(w, "decision must be approve or deny", http.StatusBadRequest)
		return
	}
	if err := gcppam.ValidateGrantName(grant); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(reason) > 512 {
		http.Error(w, "reason too long", http.StatusBadRequest)
		return
	}
	sealed, err := a.codec.sealJSON(approveState{
		Email:   SessionFrom(r.Context()).Email,
		Tier:    tier,
		Grant:   grant,
		Approve: decision == "approve",
		Reason:  reason,
		Expires: time.Now().Add(2 * time.Minute),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// include_granted_scopes makes this incremental: after the one-time
	// consent, the popup round-trips silently.
	authURL := a.consentOAuth().AuthCodeURL(sealed, oauth2.SetAuthURLParam("include_granted_scopes", "true"))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleApproveCallback finishes it: validate the sealed intent, exchange the
// code, make the single PAM call as the user, report to the opener.
func (a *App) handleApproveCallback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OAuth == nil {
		http.Error(w, "approvals are not configured", http.StatusNotImplemented)
		return
	}
	var st approveState
	if err := a.codec.openJSON(r.URL.Query().Get("state"), &st); err != nil {
		approveResult(w, false, "approval request invalid — close this window and retry")
		return
	}
	if time.Now().After(st.Expires) {
		approveResult(w, false, "approval request expired — close this window and retry")
		return
	}
	if st.Email != SessionFrom(r.Context()).Email {
		approveResult(w, false, "approval request was started by a different session")
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		// The user cancelled the consent screen (or Google rejected it).
		approveResult(w, false, "consent not granted: "+errParam)
		return
	}
	tok, err := a.consentOAuth().Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		log.Printf("ui: approve code exchange failed: %v", err)
		approveResult(w, false, "code exchange with Google failed")
		return
	}
	err = gcppam.DecideGrant(r.Context(), a.cfg.PAMBaseURL, a.cfg.QuotaProject,
		tok.AccessToken, st.Grant, st.Approve, st.Reason)
	verb := "denied"
	if st.Approve {
		verb = "approved"
	}
	if err != nil {
		// PAM's own rejection travels verbatim — a 403 here means the human
		// lacks approver IAM, and PAM's message says so better than we can.
		log.Printf("ui: pam decide (%s %s by %s): %v", verb, st.Grant, st.Email, err)
		approveResult(w, false, err.Error())
		return
	}
	log.Printf("ui: grant %s %s by %s", st.Grant, verb, st.Email)
	approveResult(w, true, "grant "+verb)
}

// approveResultTmpl reports the outcome to the opener window and closes the
// popup; without an opener it just shows the message. The payload is embedded
// as JSON (Go's encoder escapes <,>,& — safe in a script context even with
// attacker-influenced PAM error bodies).
var approveResultTmpl = template.Must(template.New("res").Parse(`<!doctype html>
<html><body style="font-family: system-ui; padding: 2em; text-align: center">
<p>{{.Message}}</p>
<script>
  var payload = {{.JSON}};
  if (window.opener) {
    window.opener.postMessage(payload, window.location.origin);
    window.close();
  }
</script>
</body></html>`))

func approveResult(w http.ResponseWriter, ok bool, message string) {
	payload, _ := json.Marshal(map[string]any{"type": "tfsp-approval", "ok": ok, "message": message})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !ok {
		w.WriteHeader(http.StatusOK) // the outcome travels in the payload; the popup itself loaded fine
	}
	if err := approveResultTmpl.Execute(w, map[string]any{
		"Message": message,
		"JSON":    template.JS(payload),
	}); err != nil {
		// The Content-Type is already text/html — escape the (PAM-error-
		// influenced) message on this fallback path too.
		fmt.Fprint(w, "approval result: ", template.HTMLEscapeString(message))
	}
}
