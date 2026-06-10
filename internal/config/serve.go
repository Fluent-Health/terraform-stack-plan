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
	Name        string
	Backend     string
	Entitlement string
	Required    bool
}

// ServeConfig is the `serve {}` block; fields are added in the serve-block task.
type ServeConfig struct{}

type serverBody struct {
	URL         string `hcl:"url,optional"`
	Environment string `hcl:"environment,optional"`
}

type classBody struct {
	Backend     string `hcl:"backend,optional"`
	Entitlement string `hcl:"entitlement,optional"`
	Required    bool   `hcl:"required,optional"`
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
	return ClassBinding{Name: blk.Labels[0], Backend: b.Backend, Entitlement: b.Entitlement, Required: b.Required}, nil
}
