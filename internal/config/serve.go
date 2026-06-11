package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ServerConfig is the `server {}` block: where the control-plane server lives and
// which environment this repo's CI reports to. Used by `run` (CI) and `serve`.
type ServerConfig struct {
	URL         string
	Environment string
}

// ClassBinding is a `class "<name>" {}` block: binds a classification class to an
// approval backend + entitlement, and whether it gates (required).
type ClassBinding struct {
	Name             string
	Backend          string
	Entitlement      string
	EntitlementScope string
	Required         bool
}

// GroupConfig configures the live-DAG grouping. Depth groups by the first Depth
// path segments (default 2 when the block is absent); Pattern (a regexp) overrides
// it — the first capture group (or whole match) of the stack path is the group key.
type GroupConfig struct {
	Depth   int
	Pattern string
}

// ObjectsConfig configures the log-offload object store (currently GCS).
type ObjectsConfig struct {
	Backend string // "gcs"
	Bucket  string
	Prefix  string
}

// PubSubConfig configures the Pub/Sub push ingestion endpoint.
type PubSubConfig struct {
	Audience       string // expected OIDC audience (default: <public_base_url>/pubsub/push)
	ServiceAccount string // the push subscription's OIDC service-account email
}

// ServeConfig is the `serve {}` block: the control-plane server runtime config.
type ServeConfig struct {
	DBPath           string
	PublicBaseURL    string
	UseChecks        bool
	WebhookSecretEnv string // env var name holding the bearer secret (not the secret itself)
	GitHubApp        *GitHubAppConfig
	Approval         *ApprovalConfig
	Group            *GroupConfig
	LogsDir          string
	Objects          *ObjectsConfig
	PubSub           *PubSubConfig
}

// GitHubAppConfig is the `github_app {}` sub-block.
type GitHubAppConfig struct {
	AppID          string
	InstallationID string
	PrivateKeyPath string
}

// ApprovalConfig is the `approval "<backend>" {}` sub-block. Entitlement ids per
// class come from the top-level `class` blocks, not here.
type ApprovalConfig struct {
	Backend       string // the block label, e.g. "gcp-pam"
	Location      string
	Duration      string
	RequesterPool []string
}

type serveBody struct {
	DBPath           string         `hcl:"db_path,optional"`
	PublicBaseURL    string         `hcl:"public_base_url,optional"`
	UseChecks        bool           `hcl:"use_checks,optional"`
	WebhookSecretEnv string         `hcl:"webhook_secret_env,optional"`
	LogsDir          string         `hcl:"logs_dir,optional"`
	GitHubApp        *githubAppBody `hcl:"github_app,block"`
	Approval         *approvalBody  `hcl:"approval,block"`
	Group            *struct {
		Depth   int    `hcl:"depth,optional"`
		Pattern string `hcl:"pattern,optional"`
	} `hcl:"group,block"`
	Objects *struct {
		Backend string `hcl:"backend,optional"`
		Bucket  string `hcl:"bucket,optional"`
		Prefix  string `hcl:"prefix,optional"`
	} `hcl:"objects,block"`
	PubSub *struct {
		Audience       string `hcl:"audience,optional"`
		ServiceAccount string `hcl:"service_account,optional"`
	} `hcl:"pubsub,block"`
}

type githubAppBody struct {
	AppID          string `hcl:"app_id,optional"`
	InstallationID string `hcl:"installation_id,optional"`
	PrivateKeyPath string `hcl:"private_key_path,optional"`
}

type approvalBody struct {
	Backend       string   `hcl:"backend,label"`
	Location      string   `hcl:"location,optional"`
	Duration      string   `hcl:"duration,optional"`
	RequesterPool []string `hcl:"requester_pool,optional"`
}

func decodeServe(blk *hclsyntax.Block) (*ServeConfig, error) {
	var b serveBody
	if d := gohcl.DecodeBody(blk.Body, nil, &b); d.HasErrors() {
		return nil, fmt.Errorf("serve block: %s", d.Error())
	}
	s := &ServeConfig{
		DBPath:           b.DBPath,
		PublicBaseURL:    b.PublicBaseURL,
		UseChecks:        b.UseChecks,
		WebhookSecretEnv: b.WebhookSecretEnv,
		LogsDir:          b.LogsDir,
	}
	if b.GitHubApp != nil {
		s.GitHubApp = &GitHubAppConfig{AppID: b.GitHubApp.AppID, InstallationID: b.GitHubApp.InstallationID, PrivateKeyPath: b.GitHubApp.PrivateKeyPath}
	}
	if b.Approval != nil {
		s.Approval = &ApprovalConfig{Backend: b.Approval.Backend, Location: b.Approval.Location, Duration: b.Approval.Duration, RequesterPool: b.Approval.RequesterPool}
	}
	if b.Group != nil {
		s.Group = &GroupConfig{Depth: b.Group.Depth, Pattern: b.Group.Pattern}
	}
	if b.Objects != nil {
		s.Objects = &ObjectsConfig{Backend: b.Objects.Backend, Bucket: b.Objects.Bucket, Prefix: b.Objects.Prefix}
	}
	if b.PubSub != nil {
		s.PubSub = &PubSubConfig{Audience: b.PubSub.Audience, ServiceAccount: b.PubSub.ServiceAccount}
	}
	return s, nil
}

type serverBody struct {
	URL         string `hcl:"url,optional"`
	Environment string `hcl:"environment,optional"`
}

type classBody struct {
	Backend          string `hcl:"backend,optional"`
	Entitlement      string `hcl:"entitlement,optional"`
	EntitlementScope string `hcl:"entitlement_scope,optional"`
	Required         bool   `hcl:"required,optional"`
}

func decodeServer(blk *hclsyntax.Block) (*ServerConfig, error) {
	var b serverBody
	if d := gohcl.DecodeBody(blk.Body, nil, &b); d.HasErrors() {
		return nil, fmt.Errorf("server block: %s", d.Error())
	}
	return &ServerConfig{URL: b.URL, Environment: b.Environment}, nil
}

func decodeClass(blk *hclsyntax.Block) (ClassBinding, error) {
	if len(blk.Labels) != 1 {
		return ClassBinding{}, fmt.Errorf("class block needs exactly one name label")
	}
	var b classBody
	if d := gohcl.DecodeBody(blk.Body, nil, &b); d.HasErrors() {
		return ClassBinding{}, fmt.Errorf("class %q: %s", blk.Labels[0], d.Error())
	}
	return ClassBinding{Name: blk.Labels[0], Backend: b.Backend, Entitlement: b.Entitlement, EntitlementScope: b.EntitlementScope, Required: b.Required}, nil
}
