package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func webhookReq(t *testing.T, srv *httptest.Server, secret, event string, payload any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/github/webhook", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", signPayload(secret, b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestGitHubWebhookRevokesOnPRClose(t *testing.T) {
	db := newServerTestDB(t)
	fake := approval.NewFake()
	a := New(db, &MockGitHub{}, Config{GitHubWebhookSecret: "webhook-secret"})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	// Establish the gate the way the reconcile core records it: drive a finalize
	// per environment through the shell so the event stream carries the requested
	// targets + their backend grant names (the PR-closed transition only revokes
	// targets carrying a grant name). gather replays this stream — seeding flat
	// gate_targets rows directly is no longer enough.
	if err := store.UpsertInit(db, events.Init{ID: "e-np", PR: 7, Environment: "nonprod", Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInit(db, events.Init{ID: "e-pr", PR: 7, Environment: "prod", Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "nonprod", "o/r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-a"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.shell.Handle(context.Background(), 7, "prod", "o/r", reconcile.RunnerFinalize{
		Gates: []events.GateTarget{{Class: "iam", Target: "proj-b"}}}); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{
		"action":       "closed",
		"pull_request": map[string]any{"number": 7, "merged": false},
	}
	resp := webhookReq(t, srv, "webhook-secret", "pull_request", payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	for _, tc := range []struct{ class, target string }{{"iam", "proj-a"}, {"iam", "proj-b"}} {
		grants, _ := fake.ListGrants(context.Background(), tc.class, tc.target)
		for _, g := range grants {
			if g.Request.PR == 7 && g.State.Open() {
				t.Errorf("grant PR 7 %s/%s still open after webhook", tc.class, tc.target)
			}
		}
	}
}

func TestGitHubWebhookLeavesMergedGrant(t *testing.T) {
	// A merged PR's grant is needed by its post-merge apply — the close webhook
	// must NOT revoke it (ApplySucceeded / PAM TTL release it).
	db := newServerTestDB(t)
	fake := approval.NewFake()
	a := New(db, &MockGitHub{}, Config{GitHubWebhookSecret: "webhook-secret"})
	a.Approval = fake
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_, _ = fake.RequestGrant(context.Background(), approval.Request{Class: "iam", Target: "proj-a", PR: 5, Environment: "nonprod"})

	payload := map[string]any{
		"action":       "closed",
		"pull_request": map[string]any{"number": 5, "merged": true},
	}
	resp := webhookReq(t, srv, "webhook-secret", "pull_request", payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	grants, _ := fake.ListGrants(context.Background(), "iam", "proj-a")
	open := false
	for _, g := range grants {
		if g.Request.PR == 5 && g.State.Open() {
			open = true
		}
	}
	if !open {
		t.Error("merged PR 5 grant must remain open (left for the apply)")
	}
}

func TestGitHubWebhookIgnoresNonCloseActions(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{GitHubWebhookSecret: "s"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	payload := map[string]any{
		"action":       "opened",
		"pull_request": map[string]any{"number": 3, "merged": false},
	}
	resp := webhookReq(t, srv, "s", "pull_request", payload)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("non-close action: status = %d, want 204", resp.StatusCode)
	}
}

func TestGitHubWebhookIgnoresNonPREvents(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{GitHubWebhookSecret: "s"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp := webhookReq(t, srv, "s", "push", map[string]any{"ref": "refs/heads/main"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("push event: status = %d, want 204", resp.StatusCode)
	}
}

func TestGitHubWebhookRejectsBadSignature(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{GitHubWebhookSecret: "correct-secret"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	b, _ := json.Marshal(map[string]any{"action": "closed", "pull_request": map[string]any{"number": 1}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/github/webhook", bytes.NewReader(b))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256=badhex")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature: status = %d, want 401", resp.StatusCode)
	}
}

func TestGitHubWebhookDisabledWhenNoSecret(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{GitHubWebhookSecret: ""})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	b := []byte(`{"action":"closed","pull_request":{"number":1}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/github/webhook", bytes.NewReader(b))
	req.Header.Set("X-GitHub-Event", "pull_request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no secret: status = %d, want 404", resp.StatusCode)
	}
}
