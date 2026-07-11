package ui

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// GitHub App webhook relay. check_run.rerequested / check_suite.rerequested
// (the Re-run buttons) are delivered ONLY to the GitHub App that owns the
// checks — never to repository webhooks — and an App has exactly one webhook
// URL while there are two tier serves. The central UI is the natural single
// ingress: this endpoint relays every delivery VERBATIM (body + signature
// headers) to every tier's /github/webhook. The UI holds no webhook secret
// and verifies nothing — each serve validates the HMAC itself, and each
// serve's handlers already ignore events for the other tier's check names.

// relayHeaders are the GitHub delivery headers a serve needs to process the
// event; everything else is dropped.
var relayHeaders = []string{"Content-Type", "X-GitHub-Event", "X-GitHub-Delivery", "X-Hub-Signature-256", "X-Hub-Signature"}

const relayBodyLimit = 2 << 20 // GitHub webhook payloads are capped at 25 MB; ours are far smaller

var relayClient = &http.Client{Timeout: 15 * time.Second}

// handleGitHubRelay is POST /github/webhook (public — GitHub authenticates
// via the HMAC the serves verify). 202 when at least one tier accepted the
// delivery; 502 when none did, so the failure is visible in the GitHub App's
// delivery log for manual redelivery.
func (a *App) handleGitHubRelay(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, relayBodyLimit+1))
	if err != nil || len(body) > relayBodyLimit {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
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
