package gcppam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
)

// TokenFunc returns an OAuth2 bearer token for the ambient identity (ADC),
// used for list/revoke. ImpersonateFunc returns a token for a specific service
// account, used for create (so PAM records the grant against the requester).
// Both are injected so the package needs no GCP client dependency; the serve
// command supplies the real implementations.
type (
	TokenFunc       func(ctx context.Context) (string, error)
	ImpersonateFunc func(ctx context.Context, serviceAccount string) (string, error)
)

// Backend is the GCP PAM approval backend.
type Backend struct {
	cfg         Config
	token       TokenFunc
	impersonate ImpersonateFunc
	hc          *http.Client
}

// New builds a gcp-pam backend. token supplies ADC bearer tokens (list/revoke);
// impersonate supplies a requester-SA token (create).
func New(cfg Config, token TokenFunc, impersonate ImpersonateFunc) *Backend {
	return &Backend{cfg: cfg, token: token, impersonate: impersonate, hc: http.DefaultClient}
}

// pamGrant is the subset of a PAM grant the backend reads.
type pamGrant struct {
	Name          string `json:"name"`
	State         string `json:"state"`
	Justification struct {
		UnstructuredJustification string `json:"unstructuredJustification"`
	} `json:"justification"`
}

type grantsList struct {
	Grants        []pamGrant `json:"grants"`
	NextPageToken string     `json:"nextPageToken"`
}

// do performs an authed PAM REST call, returning the body or an error on non-2xx.
func (b *Backend) do(ctx context.Context, bearer, method, url string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gcppam: %s %s -> %d: %s", method, url, resp.StatusCode, rb)
	}
	return rb, nil
}

// ListGrants returns the grants on the (class, target) entitlement, mapped to
// approval.Grant with state normalised and the (PR, environment) parsed from
// each grant's justification. Uses the ADC token. Follows pagination.
func (b *Backend) ListGrants(ctx context.Context, class, target string) ([]approval.Grant, error) {
	ent := b.cfg.entitlementName(class, target)
	if ent == "" {
		return nil, fmt.Errorf("gcppam: no entitlement configured for class %q", class)
	}
	tok, err := b.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcppam: ADC token: %w", err)
	}
	out := []approval.Grant{}
	pageToken := ""
	for {
		url := fmt.Sprintf("%s/%s/grants", b.cfg.baseURL(), ent)
		if pageToken != "" {
			url += "?pageToken=" + pageToken
		}
		rb, err := b.do(ctx, tok, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		var page grantsList
		if err := json.Unmarshal(rb, &page); err != nil {
			return nil, fmt.Errorf("gcppam: unmarshal grants: %w", err)
		}
		for _, g := range page.Grants {
			pr, env, ok := parsePRenv(g.Justification.UnstructuredJustification)
			req := approval.Request{Class: class, Target: target}
			if ok {
				req.PR = pr
				req.Environment = env
			}
			out = append(out, approval.Grant{Name: g.Name, State: mapState(g.State), Request: req})
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		pageToken = page.NextPageToken
	}
}
