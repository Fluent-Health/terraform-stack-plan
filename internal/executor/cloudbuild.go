package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TokenFunc returns a currently-valid OAuth2 access token (cloud-platform
// scope). Injected — same pattern as the gcp-pam approval backend — so the
// package has no GCP credential dependency and tests fake the endpoint.
type TokenFunc func(ctx context.Context) (string, error)

// CloudBuild drives Google Cloud Build triggers via the REST API
// (projects.locations.triggers.run / builds.cancel / builds.get). The trigger
// definitions stay terraform-managed; serve only runs them by name.
type CloudBuild struct {
	project  string
	region   string
	triggers map[string]string // run kind → trigger name
	token    TokenFunc
	base     string // API base, overridable for offline tests
	hc       *http.Client
}

// NewCloudBuild builds the backend. triggers maps run kinds ("plan"/"apply")
// to Cloud Build trigger names in project/region.
func NewCloudBuild(project, region string, triggers map[string]string, token TokenFunc) *CloudBuild {
	return &CloudBuild{
		project:  project,
		region:   region,
		triggers: triggers,
		token:    token,
		base:     "https://cloudbuild.googleapis.com",
		hc:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Start invokes triggers.run for the request's kind. The exact commit rides
// as commitSha; _EXECUTION_ID / _PR_NUMBER substitutions let the build report
// under the serve-minted execution id.
func (c *CloudBuild) Start(ctx context.Context, req RunRequest) (Ref, error) {
	trigger, ok := c.triggers[req.Kind]
	if !ok || trigger == "" {
		return Ref{}, fmt.Errorf("cloudbuild: no trigger configured for kind %q", req.Kind)
	}
	source := map[string]any{
		"substitutions": map[string]string{
			"_EXECUTION_ID": req.ExecutionID,
			"_PR_NUMBER":    strconv.Itoa(req.PR),
		},
	}
	// RepoSource revision is a oneof: prefer the exact commit; fall back to
	// the branch when no SHA is known.
	if req.SHA != "" {
		source["commitSha"] = req.SHA
	} else if req.Branch != "" {
		source["branchName"] = req.Branch
	}
	url := fmt.Sprintf("%s/v1/projects/%s/locations/%s/triggers/%s:run", c.base, c.project, c.region, trigger)
	body, status, err := c.do(ctx, http.MethodPost, url, map[string]any{"source": source})
	if err != nil {
		return Ref{}, err
	}
	if status/100 != 2 {
		return Ref{}, fmt.Errorf("cloudbuild: triggers.run %s: %d: %s", trigger, status, truncate(body))
	}
	// The response is a long-running Operation; the build id rides in
	// metadata.build.id.
	var op struct {
		Metadata struct {
			Build struct {
				ID string `json:"id"`
			} `json:"build"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &op); err != nil || op.Metadata.Build.ID == "" {
		return Ref{}, fmt.Errorf("cloudbuild: triggers.run %s: no build id in operation: %s", trigger, truncate(body))
	}
	return Ref{ID: op.Metadata.Build.ID}, nil
}

// Cancel stops an in-flight build. A build already finished (or being torn
// down) is treated as success — cancel is best-effort supersede cleanup.
func (c *CloudBuild) Cancel(ctx context.Context, ref Ref) error {
	if ref.ID == "" {
		return nil
	}
	url := fmt.Sprintf("%s/v1/projects/%s/locations/%s/builds/%s:cancel", c.base, c.project, c.region, ref.ID)
	body, status, err := c.do(ctx, http.MethodPost, url, map[string]any{})
	if err != nil {
		return err
	}
	// 400 "already finished" / 404 are non-errors for an idempotent cancel.
	if status/100 != 2 && status != http.StatusBadRequest && status != http.StatusNotFound {
		return fmt.Errorf("cloudbuild: builds.cancel %s: %d: %s", ref.ID, status, truncate(body))
	}
	return nil
}

// Probe reports the build's phase.
func (c *CloudBuild) Probe(ctx context.Context, ref Ref) (Phase, error) {
	url := fmt.Sprintf("%s/v1/projects/%s/locations/%s/builds/%s", c.base, c.project, c.region, ref.ID)
	body, status, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return PhaseNotFound, nil
	}
	if status/100 != 2 {
		return "", fmt.Errorf("cloudbuild: builds.get %s: %d: %s", ref.ID, status, truncate(body))
	}
	var b struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return "", fmt.Errorf("cloudbuild: builds.get %s: decode: %w", ref.ID, err)
	}
	switch b.Status {
	case "PENDING", "QUEUED":
		return PhaseQueued, nil
	case "WORKING":
		return PhaseWorking, nil
	case "SUCCESS":
		return PhaseDone, nil
	case "FAILURE", "INTERNAL_ERROR", "TIMEOUT", "EXPIRED", "CANCELLED":
		return PhaseFailed, nil
	default:
		return "", fmt.Errorf("cloudbuild: builds.get %s: unknown status %q", ref.ID, b.Status)
	}
}

// do sends an authed JSON request and returns body + status; errors only on
// transport/auth failure so callers can inspect the status.
func (c *CloudBuild) do(ctx context.Context, method, url string, payload any) ([]byte, int, error) {
	var rdr io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, 0, err
	}
	tok, err := c.token(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("cloudbuild: token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cloudbuild: %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
