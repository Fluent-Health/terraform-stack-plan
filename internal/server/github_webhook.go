package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

// handleGitHubWebhook receives GitHub webhook events at POST /github/webhook.
// Disabled (404) when GitHubWebhookSecret is empty. On a pull_request.closed
// event it revokes orphaned grants only for an abandoned (closed-unmerged) PR;
// a merged PR's grant is left for its post-merge apply (released by
// ApplySucceeded, PAM TTL as backstop).
func (a *App) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if a.cfg.GitHubWebhookSecret == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}

	if !verifyGitHubSig([]byte(a.cfg.GitHubWebhookSecret), r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	switch r.Header.Get("X-GitHub-Event") {
	case "merge_group":
		var mg struct {
			Action     string `json:"action"`
			MergeGroup struct {
				HeadSHA string `json:"head_sha"`
			} `json:"merge_group"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(body, &mg); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		a.handleMergeGroup(r.Context(), mg.Repository.FullName, mg.MergeGroup.HeadSHA, mg.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	case "pull_request":
		// existing handling continues below
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var payload struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number int  `json:"number"`
			Merged bool `json:"merged"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "closed" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	pr := payload.PullRequest.Number
	if pr <= 0 {
		http.Error(w, "invalid PR number", http.StatusBadRequest)
		return
	}
	if payload.PullRequest.Merged {
		// A merged PR's grant is consumed by the post-merge apply (released by
		// ApplySucceeded, PAM TTL as backstop) — do NOT orphan-revoke it.
		log.Printf("webhook: PR #%d merged — leaving grant for the apply", pr)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	log.Printf("webhook: PR #%d abandoned — revoking orphaned grants", pr)
	a.revokeOrphans(r.Context(), pr)
	w.WriteHeader(http.StatusNoContent)
}

// verifyGitHubSig checks that sigHeader (X-Hub-Signature-256) is a valid
// HMAC-SHA256 of body using secret.
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
