package config

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// UITierConfig is one `tier "<name>" {}` block: a tier serve the central UI
// aggregates. The UI backend authenticates to it with Google OIDC ID tokens
// minted for Audience (default: the tier URL — the audience the tier's
// api_auth block accepts by convention).
type UITierConfig struct {
	Name     string
	URL      string
	Audience string
}

// UIOAuthConfig is the `oauth {}` sub-block: the Google OAuth client the UI
// logs users in with (authorization-code flow, Workspace-internal client).
// The client secret rides an env var (name here, value in the deployment's
// secret store) like every other secret in this config.
type UIOAuthConfig struct {
	ClientID        string
	ClientSecretEnv string
	AllowedDomain   string // required Workspace domain (the id_token `hd` claim)
}

// UIConfig is the top-level `ui {}` block: the central UI service — a
// stateless aggregator over the tier serves with its own Google login.
type UIConfig struct {
	PublicBaseURL    string // external base URL (OAuth redirect URI = <base>/auth/callback)
	SessionSecretEnv string // env var name holding the session-cookie encryption secret
	Tiers            []UITierConfig
	OAuth            *UIOAuthConfig
}

type uiBody struct {
	PublicBaseURL    string `hcl:"public_base_url,optional"`
	SessionSecretEnv string `hcl:"session_secret_env,optional"`
	Tiers            []struct {
		Name     string `hcl:"name,label"`
		URL      string `hcl:"url,optional"`
		Audience string `hcl:"audience,optional"`
	} `hcl:"tier,block"`
	OAuth *struct {
		ClientID        string `hcl:"client_id,optional"`
		ClientSecretEnv string `hcl:"client_secret_env,optional"`
		AllowedDomain   string `hcl:"allowed_domain,optional"`
	} `hcl:"oauth,block"`
}

func decodeUI(blk *hclsyntax.Block) (*UIConfig, error) {
	var b uiBody
	if d := gohcl.DecodeBody(blk.Body, nil, &b); d.HasErrors() {
		return nil, fmt.Errorf("ui block: %s", d.Error())
	}
	u := &UIConfig{
		PublicBaseURL:    strings.TrimRight(b.PublicBaseURL, "/"),
		SessionSecretEnv: b.SessionSecretEnv,
	}
	if len(b.Tiers) == 0 {
		return nil, fmt.Errorf("ui: at least one tier block is required")
	}
	seen := map[string]bool{}
	for _, t := range b.Tiers {
		if seen[t.Name] {
			return nil, fmt.Errorf("ui: duplicate tier %q", t.Name)
		}
		seen[t.Name] = true
		url := strings.TrimRight(t.URL, "/")
		if url == "" {
			return nil, fmt.Errorf("ui tier %q: url is required", t.Name)
		}
		aud := t.Audience
		if aud == "" {
			aud = url
		}
		u.Tiers = append(u.Tiers, UITierConfig{Name: t.Name, URL: url, Audience: aud})
	}
	if b.OAuth != nil {
		if b.OAuth.ClientID == "" || b.OAuth.ClientSecretEnv == "" {
			return nil, fmt.Errorf("ui oauth: client_id and client_secret_env are required")
		}
		if b.OAuth.AllowedDomain == "" {
			return nil, fmt.Errorf("ui oauth: allowed_domain is required (the Workspace hd claim)")
		}
		u.OAuth = &UIOAuthConfig{
			ClientID:        b.OAuth.ClientID,
			ClientSecretEnv: b.OAuth.ClientSecretEnv,
			AllowedDomain:   strings.ToLower(b.OAuth.AllowedDomain),
		}
	}
	return u, nil
}
