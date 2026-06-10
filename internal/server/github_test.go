package server

import (
	"context"
	"testing"
)

func TestMockGitHubCountsCreateCalls(t *testing.T) {
	var gh GitHub = &MockGitHub{
		CreateCheckRunFn: func(ctx context.Context, repo, sha, environment, detailsURL string) (int64, error) {
			return 99, nil
		},
	}
	id, err := gh.CreateCheckRun(context.Background(), "o/r", "sha", "staging", "url")
	if err != nil || id != 99 {
		t.Fatalf("CreateCheckRun = %d, %v", id, err)
	}
	m := gh.(*MockGitHub)
	if m.CreateCheckRunCalls != 1 {
		t.Fatalf("CreateCheckRunCalls = %d, want 1", m.CreateCheckRunCalls)
	}
}

func TestMockGitHubDefaultsAreNoops(t *testing.T) {
	m := &MockGitHub{}
	if _, err := m.CreateCheckRun(context.Background(), "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateCheckRun(context.Background(), "", 1, CheckRunUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := m.PostStatus(context.Background(), "", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PRHeadSHA(context.Background(), "", 1); err != nil {
		t.Fatal(err)
	}
}
