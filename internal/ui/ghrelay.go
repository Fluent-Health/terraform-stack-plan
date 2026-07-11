package ui

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GitHub App webhook relay. check_run.rerequested / check_suite.rerequested
// (the Re-run buttons) are delivered ONLY to the GitHub App that owns the
// checks — never to repository webhooks — and an App has exactly one webhook
// URL while there are two tier serves. The central UI is the App's single
// ingress: it verifies GitHub's HMAC HERE (the App webhook secret exists
// nowhere else — no cross-tier shared secret) and relays each delivery to
// every tier's /github/webhook under its own Google OIDC identity, which the
// serves verify against a `webhook`-scoped principal. Each serve's handlers
// already ignore events for the other tier's check names.

// relayHeaders are the GitHub delivery headers a serve needs to process the
// event. The signature headers are deliberately NOT forwarded — authenticity
// of the relay hop is the OIDC bearer, not GitHub's HMAC (verified here).
var relayHeaders = []string{"Content-Type", "X-GitHub-Event", "X-GitHub-Delivery"}

const relayBodyLimit = 2 << 20 // GitHub webhook payloads are capped at 25 MB; ours are far smaller

var relayClient = &http.Client{Timeout: 15 * time.Second}

// handleGitHubRelay is POST /github/webhook. Disabled (404) without a
// configured App webhook secret. 202 when at least one tier accepted the
// delivery; 502 when none did, so the failure is visible in the GitHub App's
// delivery log for manual redelivery.
func (a *App) handleGitHubRelay(w http.ResponseWriter, r *http.Request) {
	if a.cfg.GitHubWebhookSecret == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, relayBodyLimit+1))
	if err != nil || len(body) > relayBodyLimit {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !verifyGitHubSig([]byte(a.cfg.GitHubWebhookSecret), r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var wg sync.WaitGroup
	oks := make([]bool, len(a.tiers))
	for i, t := range a.tiers {
		wg.Add(1)
		go func(i int, tier Tier) {
			defer wg.Done()
			oks[i] = a.relayTo(r.Context(), tier, r.Header, body)
		}(i, t)
	}
	wg.Wait()
	for _, ok := range oks {
		if ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	http.Error(w, "no tier accepted the delivery", http.StatusBadGateway)
}

func (a *App) relayTo(ctx context.Context, tier Tier, hdr http.Header, body []byte) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tier.URL+"/github/webhook", bytes.NewReader(body))
	if err != nil {
		return false
	}
	for _, h := range relayHeaders {
		if v := hdr.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if tier.Token != nil {
		bearer, terr := tier.Token(ctx)
		if terr != nil {
			log.Printf("ui: github relay to %s: mint token: %v", tier.Name, terr)
			return false
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := relayClient.Do(req)
	if err != nil {
		log.Printf("ui: github relay to %s: %v", tier.Name, err)
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		log.Printf("ui: github relay to %s: %d", tier.Name, resp.StatusCode)
		return false
	}
	return true
}

// verifyGitHubSig checks that sigHeader (X-Hub-Signature-256) is a valid
// HMAC-SHA256 of body using secret. (Mirrors the serve-side helper; the two
// packages stay decoupled over 12 lines.)
func verifyGitHubSig(secret []byte, sigHeader string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	expected, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), expected)
}
