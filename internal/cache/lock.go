package cache

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

type Provider struct {
	Address string
	Version string
}

type lockBlock struct {
	Address     string   `hcl:"address,label"`
	Version     string   `hcl:"version"`
	Constraints string   `hcl:"constraints,optional"`
	Hashes      []string `hcl:"hashes,optional"`
}

type lockSchema struct {
	Providers []lockBlock `hcl:"provider,block"`
}

func parseLockFileContent(content []byte) ([]Provider, error) {
	file, diags := hclsyntax.ParseConfig(content, ".terraform.lock.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("hcl parse: %w", diags)
	}
	var schema lockSchema
	diags = gohcl.DecodeBody(file.Body, nil, &schema)
	if diags.HasErrors() {
		return nil, fmt.Errorf("hcl decode: %w", diags)
	}
	out := make([]Provider, len(schema.Providers))
	for i, p := range schema.Providers {
		out[i] = Provider{
			Address: p.Address,
			Version: p.Version,
		}
	}
	return out, nil
}

func ParseLockFile(path string) ([]Provider, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseLockFileContent(content)
}
