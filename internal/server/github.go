package server

import "context"

// CheckRunUpdate carries everything a PATCH to a check run needs.
type CheckRunUpdate struct {
	Summary    string // GFM progress task list (renders at the top)
	Text       string // the rendered plan report (renders below the progress)
	DetailsURL string // the live page for the full view
	Conclusion string // "" while running; otherwise success|failure|action_required|neutral
}

// GitHub is the surface the server needs from GitHub. A rich check run requires
// a GitHub App with checks:write; the link-mode fallback posts a commit status
// (statuses:write) whose target URL points at the live page. The real
// implementation lands in a later sub-plan; tests use MockGitHub.
type GitHub interface {
	// CreateCheckRun opens an in_progress check run with the given name whose
	// Details link is detailsURL; returns its id.
	CreateCheckRun(ctx context.Context, repo, sha, name, detailsURL string) (int64, error)
	// UpdateCheckRun patches an existing check run.
	UpdateCheckRun(ctx context.Context, repo string, checkRunID int64, u CheckRunUpdate) error
	// PostStatus sets a commit status (link-mode fallback).
	PostStatus(ctx context.Context, repo, sha, context_, state, description, targetURL string) error
	// PRHeadSHA returns the current head commit SHA of a pull request, so a
	// verdict is posted on the live head rather than a stale execution SHA.
	PRHeadSHA(ctx context.Context, repo string, pr int) (string, error)
}

// MockGitHub is a test double for GitHub. Unset funcs are no-ops.
type MockGitHub struct {
	CreateCheckRunFn func(ctx context.Context, repo, sha, name, detailsURL string) (int64, error)
	UpdateCheckRunFn func(ctx context.Context, repo string, checkRunID int64, u CheckRunUpdate) error
	PostStatusFn     func(ctx context.Context, repo, sha, context_, state, description, targetURL string) error
	PRHeadSHAFn      func(ctx context.Context, repo string, pr int) (string, error)
	// CreateCheckRunCalls counts CreateCheckRun invocations so tests can assert
	// the check run is created exactly once (idempotency).
	CreateCheckRunCalls int
}

func (m *MockGitHub) CreateCheckRun(ctx context.Context, repo, sha, name, detailsURL string) (int64, error) {
	m.CreateCheckRunCalls++
	if m.CreateCheckRunFn != nil {
		return m.CreateCheckRunFn(ctx, repo, sha, name, detailsURL)
	}
	return 0, nil
}

func (m *MockGitHub) UpdateCheckRun(ctx context.Context, repo string, checkRunID int64, u CheckRunUpdate) error {
	if m.UpdateCheckRunFn != nil {
		return m.UpdateCheckRunFn(ctx, repo, checkRunID, u)
	}
	return nil
}

func (m *MockGitHub) PostStatus(ctx context.Context, repo, sha, context_, state, description, targetURL string) error {
	if m.PostStatusFn != nil {
		return m.PostStatusFn(ctx, repo, sha, context_, state, description, targetURL)
	}
	return nil
}

func (m *MockGitHub) PRHeadSHA(ctx context.Context, repo string, pr int) (string, error) {
	if m.PRHeadSHAFn != nil {
		return m.PRHeadSHAFn(ctx, repo, pr)
	}
	return "", nil
}
