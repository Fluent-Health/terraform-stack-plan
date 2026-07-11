package ui

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"
)

// The OAuth browser flow. Deliberately outside the OpenAPI contract — these
// are redirect endpoints, not API calls. Design (per the central-UI spec):
// authorization-code flow with scopes openid/email/profile, Workspace-domain
// (`hd`) verified server-side against the *verified* id_token — never the
// unauthenticated hint parameters — and a server-side session in an encrypted
// cookie. No Google tokens are stored; the id_token is verified and dropped.

const (
	stateCookie = "tfsp_ui_state"
	nextCookie  = "tfsp_ui_next"
)

// handleLogin starts the flow: CSRF state in a short-lived cookie, optional
// ?next= return path (local paths only), redirect to Google.
func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OAuth == nil {
		http.Error(w, "login is not configured", http.StatusNotImplemented)
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(nonce)
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/auth",
		MaxAge:   int((10 * time.Minute).Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	if next := safeLocalPath(r.URL.Query().Get("next")); next != "" {
		http.SetCookie(w, &http.Cookie{
			Name: nextCookie, Value: next, Path: "/auth",
			MaxAge:   int((10 * time.Minute).Seconds()),
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		})
	}
	http.Redirect(w, r, a.cfg.OAuth.AuthCodeURL(state), http.StatusFound)
}

// handleCallback finishes the flow: state check, code exchange, id_token
// verification (signature + audience via VerifyIDToken; email_verified and
// the Workspace hd claim here), session cookie, redirect home.
func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.OAuth == nil || a.cfg.VerifyIDToken == nil {
		http.Error(w, "login is not configured", http.StatusNotImplemented)
		return
	}
	stateC, err := r.Cookie(stateCookie)
	if err != nil || stateC.Value == "" || r.URL.Query().Get("state") != stateC.Value {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	clearCookie(w, stateCookie, "/auth")
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	tok, err := a.cfg.OAuth.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("ui: code exchange failed: %v", err)
		http.Error(w, "code exchange failed", http.StatusBadGateway)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		http.Error(w, "no id_token in token response", http.StatusBadGateway)
		return
	}
	claims, err := a.cfg.VerifyIDToken(r.Context(), rawID)
	if err != nil {
		log.Printf("ui: id_token rejected: %v", err)
		http.Error(w, "identity verification failed", http.StatusUnauthorized)
		return
	}
	email, _ := claims["email"].(string)
	if email == "" {
		http.Error(w, "identity verification failed", http.StatusUnauthorized)
		return
	}
	if verified, ok := claims["email_verified"].(bool); ok && !verified {
		http.Error(w, "identity verification failed", http.StatusUnauthorized)
		return
	}
	if hd, _ := claims["hd"].(string); !strings.EqualFold(hd, a.cfg.AllowedDomain) {
		http.Error(w, fmt.Sprintf("account is not in the %s workspace", a.cfg.AllowedDomain), http.StatusForbidden)
		return
	}
	sealed, err := a.codec.seal(Session{Email: strings.ToLower(email), Expires: time.Now().Add(sessionTTL)})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: sealed, Path: "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	next := "/"
	if c, err := r.Cookie(nextCookie); err == nil {
		if p := safeLocalPath(c.Value); p != "" {
			next = p
		}
	}
	clearCookie(w, nextCookie, "/auth")
	// Land on a 200 interstitial that then navigates, instead of setting the
	// cookie on a 302: browsers increasingly refuse cookies set on the
	// return-from-provider redirect hop (bounce-tracking mitigations, strict
	// third-party-cookie modes), while a cookie set by the top-level document
	// the user actually lands on is always first-party. Seen live: Chrome
	// stored the outbound state cookie but dropped the session cookie set on
	// the callback 302.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = signedInTmpl.Execute(w, next)
}

// signedInTmpl is the post-login landing page: it exists only to carry the
// session Set-Cookie on a 200 document response, then continues to the
// original destination. next is a validated same-origin path.
var signedInTmpl = template.Must(template.New("signedin").Parse(`<!doctype html>
<html><head><meta http-equiv="refresh" content="1;url={{.}}"></head>
<body style="font-family: system-ui; padding: 2em; text-align: center">
<p>Signed in — continuing…</p>
<script>location.replace({{.}});</script>
</body></html>`))

// handleLogout drops the session cookie. POST-only (SameSite=Lax keeps
// cross-site POSTs cookie-less, so this cannot be triggered cross-origin).
func (a *App) handleLogout(w http.ResponseWriter, _ *http.Request) {
	clearCookie(w, SessionCookie, "/")
	w.WriteHeader(http.StatusNoContent)
}

func clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// safeLocalPath admits only same-origin absolute paths ("/x", not "//evil",
// "/\evil" or full URLs), preventing open-redirect via ?next=. The check is
// the canonical leading-slash + second-character form (browsers treat both
// "//host" and "/\host" as scheme-relative).
func safeLocalPath(p string) string {
	if len(p) > 0 && p[0] == '/' && (len(p) == 1 || (p[1] != '/' && p[1] != '\\')) {
		return p
	}
	return ""
}
