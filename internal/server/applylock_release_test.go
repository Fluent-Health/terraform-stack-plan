package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// newReconcilerApplyLockApp builds an app on the production path: reconciler core
// + apply-lock both on.
func newReconcilerApplyLockApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	db := newServerTestDB(t)
	a := New(db, &recordingGitHub{}, Config{PublicBaseURL: "https://srv"})
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	return a, srv
}

// classifyGate plans (no gates) so the changeset for (pr,env) is classified
// Clean — the precondition for ApplySucceeded to release (a never-classified
// changeset has no apply and no claim).
func classifyGate(t *testing.T, srv *httptest.Server, pr int, env string) {
	t.Helper()
	post(t, srv, "/api/init", events.Init{ID: "plan-1", Repo: "o/r", SHA: "sha", PR: pr, Environment: env,
		Stacks: []events.StackState{{Path: "a", Status: events.StatusPlanned}}})
	post(t, srv, "/api/finalize", events.Finalize{ID: "plan-1", ReportMarkdown: "# r"})
}

// TestApplyEndGateRevokeReleasesClaim: the apply's terminal GateRevoke flows to
// reconcile.ApplySucceeded → ReleaseClaim → the claim is released. This is the
// real release path now that the finalize handler no longer releases.
func TestApplyEndGateRevokeReleasesClaim(t *testing.T) {
	a, srv := newReconcilerApplyLockApp(t)
	classifyGate(t, srv, 7, "staging")

	// PR merged ⇒ claimed its stack.
	if err := store.ClaimStacks(a.db, "staging", 7, "apply-1", []string{"a"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Apply finishes ⇒ runner posts GateRevoke (apply.go:227).
	if code := post(t, srv, "/api/gate/revoke", events.GateRevoke{PR: 7, Environment: "staging"}); code != 200 {
		t.Fatalf("gate/revoke = %d", code)
	}
	if c, _ := store.ClaimedStacks(a.db, "staging", time.Now()); len(c) != 0 {
		t.Fatalf("apply-end GateRevoke did not release the claim: %v", c)
	}
}

// TestClassifyPassFinalizeKeepsClaim is the regression for the premature-release
// bug: run apply emits a mid-run Finalize during its classify pass (apply
// context, not failed, stacks still pending — the state move + terramate apply
// have not run). That finalize must NOT release the merge-lock claim.
func TestClassifyPassFinalizeKeepsClaim(t *testing.T) {
	a, srv := newReconcilerApplyLockApp(t)

	post(t, srv, "/api/init", events.Init{ID: "apply-1", Repo: "o/r", SHA: "sha", PR: 7, Environment: "staging",
		Context: "apply/staging", Stacks: []events.StackState{{Path: "a", Status: events.StatusPending}}})
	if err := store.ClaimStacks(a.db, "staging", 7, "apply-1", []string{"a"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The classify-pass Finalize (Gates re-classify), stacks still pending.
	if code := post(t, srv, "/api/finalize", events.Finalize{ID: "apply-1", ReportMarkdown: "# r"}); code != 200 {
		t.Fatalf("classify finalize = %d", code)
	}
	if c, _ := store.ClaimedStacks(a.db, "staging", time.Now()); len(c) == 0 {
		t.Fatal("classify-pass finalize released the claim before the apply ran")
	}
}
