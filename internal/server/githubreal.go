package server

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiBase is the GitHub REST base; overridable in tests.
var apiBase = "https://api.github.com"

// RealClient is the production GitHub implementation. It mints a fresh App
// installation token per request, so the token never goes stale on a
// long-running service. The App key + ids are supplied by the caller (a
// deployment concern); RealClient has no cloud-secret-store dependency.
type RealClient struct {
	appID          string
	installationID string
	key            *rsa.PrivateKey
}

var _ GitHub = (*RealClient)(nil)

// NewRealClient parses the PEM App private key and returns a client for the
// given App id + installation id.
func NewRealClient(appID, installationID string, pemKey []byte) (*RealClient, error) {
	if appID == "" || installationID == "" {
		return nil, errors.New("github: app id and installation id are required")
	}
	key, err := parseRSAPrivateKey(pemKey)
	if err != nil {
		return nil, err
	}
	return &RealClient{appID: appID, installationID: installationID, key: key}, nil
}

// mintToken exchanges a fresh App JWT for an installation access token.
func (c *RealClient) mintToken(ctx context.Context) (string, error) {
	jwtTok, err := appJWT(c.appID, c.key, time.Now())
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%s/access_tokens", apiBase, c.installationID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtTok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github: installation token exchange %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// do performs an authed GitHub REST call, minting a fresh installation token.
// A nil payload sends no body (for GET).
func (c *RealClient) do(ctx context.Context, method, url string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	tok, err := c.mintToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("github: resolve token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("github: %s %s: %d: %s", method, url, resp.StatusCode, rb)
	}
	return rb, nil
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github: invalid repo %q (want owner/name)", repo)
	}
	return parts[0], parts[1], nil
}

// output builds the check-run output object as pure GFM: the progress task list
// (summary) and the rendered report (text). No embedded image — GitHub tiles
// small SVG check-run images, so the diagram lives on the live page.
func output(title, summary, text string) map[string]any {
	out := map[string]any{
		"title":   title,
		"summary": summary,
	}
	if text != "" {
		out["text"] = text
	}
	return out
}

// checkRunName is the per-environment check-run name (the gate surface that
// branch protection requires): "plan/<environment>" (or "plan" when empty).
func checkRunName(environment string) string {
	if environment == "" {
		return "plan"
	}
	return "plan/" + environment
}

// CreateCheckRun opens an in_progress check run with the given name.
func (c *RealClient) CreateCheckRun(ctx context.Context, repo, sha, name, detailsURL string) (int64, error) {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{
		"name":     name,
		"head_sha": sha,
		"status":   "in_progress",
		"output":   output("Terraform plan", "Planning…", ""),
	}
	if detailsURL != "" {
		payload["details_url"] = detailsURL
	}
	rb, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/%s/check-runs", apiBase, owner, repoName), payload)
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// UpdateCheckRun patches a check run. A non-empty Conclusion completes the run.
func (c *RealClient) UpdateCheckRun(ctx context.Context, repo string, checkRunID int64, u CheckRunUpdate) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]any{"output": output(u.Title, u.Summary, u.Text)}
	if u.DetailsURL != "" {
		payload["details_url"] = u.DetailsURL
	}
	if u.Conclusion != "" {
		payload["status"] = "completed"
		payload["conclusion"] = u.Conclusion
	}
	_, err = c.do(ctx, http.MethodPatch,
		fmt.Sprintf("%s/repos/%s/%s/check-runs/%d", apiBase, owner, name, checkRunID), payload)
	return err
}

// PostStatus sets a commit status (commit-status fallback; needs only statuses:write).
func (c *RealClient) PostStatus(ctx context.Context, repo, sha, context_, state, description, targetURL string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	payload := map[string]string{"state": state, "context": context_, "description": description}
	if targetURL != "" {
		payload["target_url"] = targetURL
	}
	_, err = c.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/%s/statuses/%s", apiBase, owner, name, sha), payload)
	return err
}

// PRHeadSHA returns a pull request's current head commit SHA.
func (c *RealClient) PRHeadSHA(ctx context.Context, repo string, pr int) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	rb, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, pr), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", err
	}
	if out.Head.SHA == "" {
		return "", fmt.Errorf("github: no head sha for PR #%d", pr)
	}
	return out.Head.SHA, nil
}

// PRAbandoned reports whether the PR is closed without having been merged.
func (c *RealClient) PRAbandoned(ctx context.Context, repo string, pr int) (bool, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	rb, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, pr), nil)
	if err != nil {
		return false, err
	}
	var out struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return false, err
	}
	return out.State == "closed" && !out.Merged, nil
}

// MergeGroupPRs returns the PR numbers whose commits compose the merge group at
// headSHA, via the commit→PRs association API.
func (c *RealClient) MergeGroupPRs(ctx context.Context, repo, headSHA string) ([]int, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	body, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/commits/%s/pulls", apiBase, owner, name, headSHA), nil)
	if err != nil {
		return nil, err
	}
	var pulls []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(body, &pulls); err != nil {
		return nil, err
	}
	out := make([]int, 0, len(pulls))
	for _, p := range pulls {
		out = append(out, p.Number)
	}
	return out, nil
}

// applyLockName is the per-environment apply-lock check name (the merge gate
// that branch protection / the merge queue require): "apply-lock/<env>".
func applyLockName(environment string) string {
	if environment == "" {
		return "apply-lock"
	}
	return "apply-lock/" + environment
}

// ReRequestCheckRun triggers a check-run re-request.
func (c *RealClient) ReRequestCheckRun(ctx context.Context, repo string, checkRunID int64) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/%s/check-runs/%d/rerequest", apiBase, owner, repoName, checkRunID)
	_, err = c.do(ctx, http.MethodPost, url, nil)
	return err
}
