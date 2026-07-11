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
// URL while there may be several tier serves. The relay is a deliberately
// DUMB pipe: each delivery is forwarded VERBATIM, signature headers included,
// to every tier's /github/webhook, and each serve verifies GitHub's HMAC
// itself. Authenticity stays end-to-end — a compromised relay cannot forge
// events — and a single-tier deployment can skip the relay entirely by
// pointing the App webhook straight at its serve.
//
// Defense in depth (optional): when ui { github_webhook_secret_env } is set,
// the relay ALSO verifies the HMAC before fanning out, so garbage dies here
// and shows as a 401 in the App's delivery log. When unset, the relay
// forwards blindly and the serves reject.

// relayHeaders are the GitHub delivery headers the serves need — including
// the signatures they verify.
var relayHeaders = []string{"Content-Type", "X-GitHub-Event", "X-GitHub-Delivery", "X-Hub-Signature-256", "X-Hub-Signature"}

const relayBodyLimit = 2 << 20 // GitHub webhook payloads are capped at 25 MB; ours are far smaller

var relayClient = &http.Client{Timeout: 15 * time.Second}

// handleGitHubRelay is POST /github/webhook (public — authenticity is
// GitHub's HMAC, verified by every serve and optionally here too). 202 when
// at least one tier accepted the delivery; 502 when none did, so the failure
// is visible in the GitHub App's delivery log for manual redelivery.
func (a *App) handleGitHubRelay(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, relayBodyLimit+1))
	if err != nil || len(body) > relayBodyLimit {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if a.cfg.GitHubWebhookSecret != "" &&
		!verifyGitHubSig([]byte(a.cfg.GitHubWebhookSecret), r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var wg sync.WaitGroup
	oks := make([]bool, len(a.tiers))
	for i, t := range a.tiers {
		wg.Add(1)
		go func(i int, tier Tier) {
			defer wg.Done()
			oks[i] = relayTo(r.Context(), tier, r.Header, body)
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

func relayTo(ctx context.Context, tier Tier, hdr http.Header, body []byte) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tier.URL+"/github/webhook", bytes.NewReader(body))
	if err != nil {
		return false
	}
	for _, h := range relayHeaders {
		if v := hdr.Get(h); v != "" {
			req.Header.Set(h, v)
		}
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
