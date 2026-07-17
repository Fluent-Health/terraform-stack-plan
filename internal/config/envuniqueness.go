package config

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Catalyst defaults for a SourceBlock left unspecified (or the whole
// `source {}` block omitted): where per-stack instance manifests live and
// which config paths inside them carry environment names / input values.
const (
	defaultSourceGlob             = "components/**/instances/*.tm.yml"
	defaultSourceEnvironmentsPath = "environments"
	defaultSourceInputsPath       = "inputs"

	defaultProtectedTier = "prod"
)

// EnvUniquenessConfig is the `env_uniqueness {}` block: config for the
// generic per-environment-value duplication lint. It declares the known
// environments (and which tier each belongs to), where to read per-env input
// values from, and any reviewed exceptions (`allow` blocks) that should not
// be flagged.
type EnvUniquenessConfig struct {
	ProtectedTier   string              `hcl:"protected_tier,optional"`
	ProjectTemplate string              `hcl:"project_token_template,optional"`
	Environments    []EnvBlock          `hcl:"environment,block"`
	Source          *SourceBlock        `hcl:"source,block"`
	ExtraEnvTokens  map[string][]string `hcl:"extra_env_tokens,optional"`
	ExtraScopedSegs []string            `hcl:"extra_scoped_segments,optional"`
	ExtraKeyPats    []string            `hcl:"extra_key_patterns,optional"`
	Allows          []AllowBlock        `hcl:"allow,block"`
}

// EnvBlock is one `environment "<name>" { tier = "<tier>" }` block: declares
// an environment and the tier it belongs to (e.g. "dev" is "nonprod").
type EnvBlock struct {
	Name string `hcl:"name,label"`
	Tier string `hcl:"tier"`
}

// SourceBlock is the `source {}` sub-block: where per-stack instance
// manifests live (Glob) and which config paths inside them carry the
// environment name / input values the lint compares.
type SourceBlock struct {
	Glob             string `hcl:"glob,optional"`
	EnvironmentsPath string `hcl:"environments_path,optional"`
	InputsPath       string `hcl:"inputs_path,optional"`
	TierInput        string `hcl:"tier_input,optional"`
}

// AllowBlock is one `allow {}` exception: a reviewed, justified duplicate
// value for a given (unit, key) across the listed envs. Reason is mandatory
// so every exception carries an auditable justification.
type AllowBlock struct {
	Unit    string   `hcl:"unit"`
	Key     string   `hcl:"key"`
	Envs    []string `hcl:"envs"`
	Reason  string   `hcl:"reason"`
	Expires string   `hcl:"expires,optional"`
}

// decodeEnvUniqueness decodes the `env_uniqueness {}` block directly into
// EnvUniquenessConfig (its hcl tags describe the schema), then applies
// defaults and validates that every allow block carries a real reason.
func decodeEnvUniqueness(blk *hclsyntax.Block) (*EnvUniquenessConfig, error) {
	var c EnvUniquenessConfig
	if d := gohcl.DecodeBody(blk.Body, nil, &c); d.HasErrors() {
		return nil, fmt.Errorf("env_uniqueness block: %s", d.Error())
	}

	if c.ProtectedTier == "" {
		c.ProtectedTier = defaultProtectedTier
	}
	if c.Source == nil {
		c.Source = &SourceBlock{}
	}
	if c.Source.Glob == "" {
		c.Source.Glob = defaultSourceGlob
	}
	if c.Source.EnvironmentsPath == "" {
		c.Source.EnvironmentsPath = defaultSourceEnvironmentsPath
	}
	if c.Source.InputsPath == "" {
		c.Source.InputsPath = defaultSourceInputsPath
	}

	for _, a := range c.Allows {
		if strings.TrimSpace(a.Reason) == "" {
			return nil, fmt.Errorf("env_uniqueness: allow %s.%s: reason is required", a.Unit, a.Key)
		}
	}

	return &c, nil
}
