package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// A post-merge apply execution (apply/<env> context) must surface on GitHub as a
// commit status linking to the live page — NOT a check run (check runs on the
// default branch dangle). Even with UseChecks=true, drive routes apply contexts
// to the status writer.
func TestDriveApplyPostsApplyStatusNotCheckRun(t *testing.T) {
	db := newServerTestDB(t)
	var gotCtx, gotState, gotURL string
	createdCheckRun := false
	gh := &MockGitHub{
		PostStatusFn: func(_ context.Context, _, _, c, state, _, url string) error {
			gotCtx, gotState, gotURL = c, state, url
			return nil
		},
		CreateCheckRunFn: func(_ context.Context, _, _, _, _ string) (int64, error) {
			createdCheckRun = true
			return 1, nil
		},
	}
	a := New(db, gh, Config{UseChecks: true, PublicBaseURL: "https://serve.example"})

	if err := store.UpsertInit(db, events.Init{
		ID: "apply-1", Repo: "o/r", SHA: "deadbeef", PR: 7, Environment: "nonprod",
		Context: "apply/nonprod",
		Stacks:  []events.StackState{{Path: "a", Status: events.StatusSafe}},
	}); err != nil {
		t.Fatal(err)
	}

	a.drive(context.Background(), "apply-1", "https://serve.example", true)

	if gotCtx != "apply/nonprod" {
		t.Fatalf("status context = %q, want apply/nonprod", gotCtx)
	}
	if gotState == "" {
		t.Fatalf("no apply status posted")
	}
	if gotURL == "" {
		t.Fatalf("apply status has no targetURL (should link to /live)")
	}
	if createdCheckRun {
		t.Fatalf("apply execution must NOT create a check run")
	}
}
