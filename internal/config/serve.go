package config

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ServerConfig is the `server {}` block: where the control-plane server lives and
// which environment this repo's CI reports to. Used by `run` (CI) and `serve`.
type ServerConfig struct {
	Name        string
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

// API scopes grantable to an api_auth principal. Each /api/* route on the
// server accepts a subset of these (any-of). Validated at config load so a
// typo'd scope fails at startup, not as runtime 403s.
const (
	ScopeReport = "report" // CI runner: execution lifecycle events, logs, gates, claims
	ScopeRead   = "read"   // read-only: execution state/events, claims listing
	ScopeAdmin  = "admin"  // operator surgery: claim release (and future admin verbs)
)

// APIAuthPrincipal maps one verified caller identity (an email — service
// account or user) to the API scopes it holds.
type APIAuthPrincipal struct {
	Email  string
	Scopes []string
}

// APIAuthConfig configures Google OIDC bearer auth for /api/*: which token
// audiences are accepted and the identity → scope allowlist. It is the only
// /api/* auth — the legacy shared-secret HS256 path was removed once every
// caller migrated to OIDC.
type APIAuthConfig struct {
	Audience       string   // expected OIDC audience (default: public_base_url)
	ExtraAudiences []string // additional accepted audiences (e.g. the gcloud ADC client id, for user tokens)
	Principals     []APIAuthPrincipal
}

// ExecutorConfig is the `executor "<backend>" {}` sub-block: the CI backend
// serve drives when it triggers runs itself (webhook → build). Only
// "cloudbuild" is implemented; the trigger definitions stay terraform-managed,
// serve runs them by name.
type ExecutorConfig struct {
	Backend  string // the block label, e.g. "cloudbuild"
	Project  string
	Region   string
	Triggers map[string]string // run kind ("plan"/"apply") → trigger name
}

// ServeConfig is the `serve {}` block: the control-plane server runtime config.
type ServeConfig struct {
	DBPath                 string
	PublicBaseURL          string
	GitHubWebhookSecretEnv string // env var name holding the GitHub webhook HMAC secret
	// UIBaseURL is the central UI service's external base URL. Check-run
	// details and approval links point there ("" leaves them unset — the
	// check-run body still carries everything). The UI's tier names must
	// match the serve environments for its /t/<tier>/e/<id> routes.
	UIBaseURL string
	GitHubApp *GitHubAppConfig
	Approval  *ApprovalConfig
	LogsDir   string
	Objects   *ObjectsConfig
	PubSub    *PubSubConfig
	APIAuth   *APIAuthConfig
	Executor  *ExecutorConfig
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
	DBPath                 string         `hcl:"db_path,optional"`
	PublicBaseURL          string         `hcl:"public_base_url,optional"`
	GitHubWebhookSecretEnv string         `hcl:"github_webhook_secret_env,optional"`
	UIBaseURL              string         `hcl:"ui_base_url,optional"`
	LogsDir                string         `hcl:"logs_dir,optional"`
	GitHubApp              *githubAppBody `hcl:"github_app,block"`
	Approval               *approvalBody  `hcl:"approval,block"`
	Objects                *struct {
		Backend string `hcl:"backend,optional"`
		Bucket  string `hcl:"bucket,optional"`
		Prefix  string `hcl:"prefix,optional"`
	} `hcl:"objects,block"`
	PubSub *struct {
		Audience       string `hcl:"audience,optional"`
		ServiceAccount string `hcl:"service_account,optional"`
	} `hcl:"pubsub,block"`
	APIAuth *struct {
		Audience       string   `hcl:"audience,optional"`
		ExtraAudiences []string `hcl:"extra_audiences,optional"`
		Principals     []struct {
			Email  string   `hcl:"email,label"`
			Scopes []string `hcl:"scopes,optional"`
		} `hcl:"principal,block"`
	} `hcl:"api_auth,block"`
	Executor *struct {
		Backend  string `hcl:"backend,label"`
		Project  string `hcl:"project,optional"`
		Region   string `hcl:"region,optional"`
		Triggers []struct {
			Kind string `hcl:"kind,label"`
			Name string `hcl:"name,optional"`
		} `hcl:"trigger,block"`
	} `hcl:"executor,block"`
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
		DBPath:                 b.DBPath,
		PublicBaseURL:          b.PublicBaseURL,
		UIBaseURL:              strings.TrimRight(b.UIBaseURL, "/"),
		GitHubWebhookSecretEnv: b.GitHubWebhookSecretEnv,
		LogsDir:                b.LogsDir,
	}
	if b.GitHubApp != nil {
		s.GitHubApp = &GitHubAppConfig{AppID: b.GitHubApp.AppID, InstallationID: b.GitHubApp.InstallationID, PrivateKeyPath: b.GitHubApp.PrivateKeyPath}
	}
	if b.Approval != nil {
		s.Approval = &ApprovalConfig{Backend: b.Approval.Backend, Location: b.Approval.Location, Duration: b.Approval.Duration, RequesterPool: b.Approval.RequesterPool}
	}
	if b.Objects != nil {
		s.Objects = &ObjectsConfig{Backend: b.Objects.Backend, Bucket: b.Objects.Bucket, Prefix: b.Objects.Prefix}
	}
	if b.PubSub != nil {
		s.PubSub = &PubSubConfig{Audience: b.PubSub.Audience, ServiceAccount: b.PubSub.ServiceAccount}
	}
	if b.APIAuth != nil {
		aa := &APIAuthConfig{Audience: b.APIAuth.Audience, ExtraAudiences: b.APIAuth.ExtraAudiences}
		seen := map[string]bool{}
		for _, p := range b.APIAuth.Principals {
			email := strings.ToLower(p.Email)
			if seen[email] {
				return nil, fmt.Errorf("api_auth: duplicate principal %q", p.Email)
			}
			seen[email] = true
			for _, sc := range p.Scopes {
				switch sc {
				case ScopeReport, ScopeRead, ScopeAdmin:
				default:
					return nil, fmt.Errorf("api_auth principal %q: unknown scope %q (valid: %s, %s, %s)", p.Email, sc, ScopeReport, ScopeRead, ScopeAdmin)
				}
			}
			aa.Principals = append(aa.Principals, APIAuthPrincipal{Email: p.Email, Scopes: p.Scopes})
		}
		s.APIAuth = aa
	}
	if b.Executor != nil {
		if b.Executor.Backend != "cloudbuild" {
			return nil, fmt.Errorf("executor: unknown backend %q (only \"cloudbuild\" is implemented)", b.Executor.Backend)
		}
		if b.Executor.Project == "" || b.Executor.Region == "" {
			return nil, fmt.Errorf("executor %q: project and region are required", b.Executor.Backend)
		}
		ex := &ExecutorConfig{
			Backend: b.Executor.Backend, Project: b.Executor.Project, Region: b.Executor.Region,
			Triggers: map[string]string{},
		}
		for _, tr := range b.Executor.Triggers {
			switch tr.Kind {
			case "plan", "apply":
			default:
				return nil, fmt.Errorf("executor %q: unknown trigger kind %q (valid: plan, apply)", b.Executor.Backend, tr.Kind)
			}
			if tr.Name == "" {
				return nil, fmt.Errorf("executor %q: trigger %q needs a name", b.Executor.Backend, tr.Kind)
			}
			if _, dup := ex.Triggers[tr.Kind]; dup {
				return nil, fmt.Errorf("executor %q: duplicate trigger %q", b.Executor.Backend, tr.Kind)
			}
			ex.Triggers[tr.Kind] = tr.Name
		}
		if ex.Triggers["plan"] == "" || ex.Triggers["apply"] == "" {
			return nil, fmt.Errorf("executor %q: both a plan and an apply trigger are required", b.Executor.Backend)
		}
		s.Executor = ex
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
	name := ""
	if len(blk.Labels) > 0 {
		name = blk.Labels[0]
	}
	return &ServerConfig{Name: name, URL: b.URL, Environment: b.Environment}, nil
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
