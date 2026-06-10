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

// CreateCheckRun, UpdateCheckRun, and PostStatus are implemented in Task 3.
// Stubs here satisfy the GitHub interface so the compiler accepts RealClient.
func (c *RealClient) CreateCheckRun(_ context.Context, _, _, _, _ string) (int64, error) {
	return 0, errors.New("github: CreateCheckRun not yet implemented")
}

func (c *RealClient) UpdateCheckRun(_ context.Context, _ string, _ int64, _ CheckRunUpdate) error {
	return errors.New("github: UpdateCheckRun not yet implemented")
}

func (c *RealClient) PostStatus(_ context.Context, _, _, _, _, _, _ string) error {
	return errors.New("github: PostStatus not yet implemented")
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
