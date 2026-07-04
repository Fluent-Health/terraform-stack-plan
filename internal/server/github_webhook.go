package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
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
	case "push":
		a.handlePushEventWebhook(w, r, body)
		return
	case "check_run":
		a.handleCheckRunWebhook(w, r, body)
		return
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
		if err := a.handleMergeGroup(r.Context(), mg.Repository.FullName, mg.MergeGroup.HeadSHA, mg.Action); err != nil {
			http.Error(w, "merge_group eval failed", http.StatusInternalServerError)
			return
		}
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
			Head   struct {
				SHA string `json:"sha"`
				Ref string `json:"ref"`
			} `json:"head"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	pr := payload.PullRequest.Number
	repoFullName := payload.Repository.FullName

	switch payload.Action {
	case "opened", "reopened", "synchronize":
		if pr > 0 {
			a.handlePRApplyLock(r.Context(), repoFullName, pr, false)
			// Serve-as-driver: request the plan run for the new head. Inside the
			// webhook turnaround, so the check + live link appear before any
			// build machine spins up. A redelivery no-ops in the decider.
			if a.runTriggerArmed() {
				if err := a.shell.Handle(r.Context(), pr, a.cfg.Environment, repoFullName, reconcile.RunRequested{
					Kind:   reconcile.RunKindPlan,
					SHA:    payload.PullRequest.Head.SHA,
					Branch: payload.PullRequest.Head.Ref,
				}); err != nil {
					log.Printf("webhook: plan run request pr=%d: %v", pr, err)
					http.Error(w, "run request failed", http.StatusInternalServerError)
					return
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	case "closed":
		// handled below
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if pr <= 0 {
		http.Error(w, "invalid PR number", http.StatusBadRequest)
		return
	}
	if payload.PullRequest.Merged {
		// A merged PR's grant is consumed by the post-merge apply (released by
		// ApplySucceeded, PAM TTL as backstop) — do NOT orphan-revoke it.
		log.Printf("webhook: PR #%d merged — leaving grant for the apply", pr)
		a.handlePRApplyLock(r.Context(), repoFullName, pr, true)
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

// runTriggerArmed reports whether serve drives CI runs itself: an executor
// backend is wired and the tier environment is known.
func (a *App) runTriggerArmed() bool {
	return a.Executor != nil && a.cfg.Environment != ""
}

// prFromMergeSubject recovers the PR number from a merge-commit subject line.
// Two formats: squash "<title> (#N)" and merge "Merge pull request #N from …".
// Returns 0 when neither matches (e.g. a direct push).
func prFromMergeSubject(subject string) int {
	if m := squashSubjectRE.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := mergeSubjectRE.FindStringSubmatch(subject); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

var (
	squashSubjectRE = regexp.MustCompile(`\(#([0-9]+)\)`)
	mergeSubjectRE  = regexp.MustCompile(`^Merge pull request #([0-9]+) `)
)

// handlePushEventWebhook requests the post-merge apply run for a push to main.
// The PR number is recovered from the merge-commit subject (same convention as
// the CI apply build); a push with no recoverable PR is skipped — there is no
// changeset to correlate the gate to.
func (a *App) handlePushEventWebhook(w http.ResponseWriter, r *http.Request, body []byte) {
	if !a.runTriggerArmed() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var p struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		HeadCommit struct {
			Message string `json:"message"`
		} `json:"head_commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if p.Ref != "refs/heads/main" || p.After == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	subject, _, _ := strings.Cut(p.HeadCommit.Message, "\n")
	pr := prFromMergeSubject(subject)
	if pr == 0 {
		log.Printf("webhook: push %.12s to main has no recoverable PR — skipping apply run", p.After)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.shell.Handle(r.Context(), pr, a.cfg.Environment, p.Repository.FullName, reconcile.RunRequested{
		Kind:   reconcile.RunKindApply,
		SHA:    p.After,
		Branch: "main",
	}); err != nil {
		log.Printf("webhook: apply run request pr=%d: %v", pr, err)
		http.Error(w, "run request failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCheckRunWebhook re-requests the matching run when GitHub's native
// Re-run button is pressed on one of THIS serve's check runs. Other tiers'
// checks (and unrelated apps') are ignored.
func (a *App) handleCheckRunWebhook(w http.ResponseWriter, r *http.Request, body []byte) {
	if !a.runTriggerArmed() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var p struct {
		Action   string `json:"action"`
		CheckRun struct {
			Name         string `json:"name"`
			HeadSHA      string `json:"head_sha"`
			PullRequests []struct {
				Number int `json:"number"`
			} `json:"pull_requests"`
		} `json:"check_run"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if p.Action != "rerequested" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var kind string
	switch p.CheckRun.Name {
	case checkRunName(a.cfg.Environment):
		kind = reconcile.RunKindPlan
	case runContext(reconcile.RunKindApply, a.cfg.Environment):
		kind = reconcile.RunKindApply
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(p.CheckRun.PullRequests) == 0 {
		log.Printf("webhook: check_run rerequested for %s %.12s carries no PR — skipping", p.CheckRun.Name, p.CheckRun.HeadSHA)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	pr := p.CheckRun.PullRequests[0].Number
	if err := a.shell.Handle(r.Context(), pr, a.cfg.Environment, p.Repository.FullName, reconcile.RunRequested{
		Kind:  kind,
		SHA:   p.CheckRun.HeadSHA,
		Rerun: true,
	}); err != nil {
		log.Printf("webhook: rerun request pr=%d kind=%s: %v", pr, kind, err)
		http.Error(w, "run request failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
