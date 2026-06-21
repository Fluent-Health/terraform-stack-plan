package server

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func ctx() context.Context { return context.Background() }

func newApplyLockTestApp(t *testing.T) (*App, *recordingGitHub) {
	t.Helper()
	db := newServerTestDB(t)
	gh := &recordingGitHub{}
	a := New(db, gh, Config{ApplyLock: true, PublicBaseURL: "https://srv"})
	return a, gh
}

// recordingGitHub is a test double that records the last UpdateCheckRun call.
type recordingGitHub struct {
	lastUpdate CheckRunUpdate
}

func (r *recordingGitHub) CreateCheckRun(_ context.Context, _, _, _ string, _ string) (int64, error) {
	return 99, nil
}

func (r *recordingGitHub) UpdateCheckRun(_ context.Context, _ string, _ int64, u CheckRunUpdate) error {
	r.lastUpdate = u
	return nil
}

func (r *recordingGitHub) PostStatus(_ context.Context, _, _, _, _, _, _ string) error {
	return nil
}

func (r *recordingGitHub) PRHeadSHA(_ context.Context, _ string, _ int) (string, error) {
	return "", nil
}

func (r *recordingGitHub) PRAbandoned(_ context.Context, _ string, _ int) (bool, error) {
	return false, nil
}

func TestPostApplyLock(t *testing.T) {
	a, gh := newApplyLockTestApp(t)
	// held verdict => check created, left in_progress (no conclusion), record persisted held.
	v := applyLockVerdict{State: "held", Blocking: []string{"a"}, Reason: "x"}
	if err := a.postApplyLock(ctx(), "o/r", "prod", "sha1", 7, []string{"a"}, "merge_group", v); err != nil {
		t.Fatal(err)
	}
	if gh.lastUpdate.Conclusion != "" {
		t.Errorf("held check should have empty conclusion, got %q", gh.lastUpdate.Conclusion)
	}
	rec, ok, _ := store.GetApplyLockCheck(a.db, "prod", "sha1")
	if !ok || rec.State != "held" {
		t.Fatalf("record = %+v ok=%v, want held", rec, ok)
	}
	// clear verdict => conclusion success.
	_ = a.postApplyLock(ctx(), "o/r", "prod", "sha1", 7, []string{"a"}, "merge_group", applyLockVerdict{State: "clear"})
	if gh.lastUpdate.Conclusion != "success" {
		t.Errorf("clear check conclusion = %q, want success", gh.lastUpdate.Conclusion)
	}
}

func TestOverlap(t *testing.T) {
	claimed := map[string]int{"a": 5, "b": 5, "c": 9}
	// PR 7 touching b,d => b is claimed by another PR (5) => blocking.
	got := overlap(claimed, []string{"b", "d"}, 7)
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("overlap = %v, want [b]", got)
	}
	// A PR's own claim does not block itself.
	if g := overlap(claimed, []string{"a"}, 5); len(g) != 0 {
		t.Fatalf("self-claim blocked: %v", g)
	}
	// Disjoint => no overlap.
	if g := overlap(claimed, []string{"d", "e"}, 7); len(g) != 0 {
		t.Fatalf("disjoint blocked: %v", g)
	}
}
