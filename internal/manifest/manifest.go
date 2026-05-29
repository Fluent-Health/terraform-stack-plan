// Package manifest loads the per-run stack list, either from a YAML/JSON
// manifest file or from repeated --stack NAME:PATH flags.
package manifest

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// StackRef names a stack and the path to its plan JSON.
type StackRef struct {
	Name string `yaml:"name" json:"name"`
	Plan string `yaml:"plan" json:"plan"`
}

// Manifest is the per-run input document.
type Manifest struct {
	Title  string     `yaml:"title" json:"title"`
	Marker string     `yaml:"marker" json:"marker"`
	Stacks []StackRef `yaml:"stacks" json:"stacks"`
}

// Load reads a YAML or JSON manifest (YAML is a superset of JSON, so yaml.v3
// parses both).
func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if len(m.Stacks) == 0 {
		return Manifest{}, fmt.Errorf("manifest %s: no stacks", path)
	}
	return m, nil
}

// ParseStackFlags turns "NAME:PATH" strings into StackRefs. NAME may contain
// slashes; only the first ':' separates name from path.
func ParseStackFlags(flags []string) ([]StackRef, error) {
	var refs []StackRef
	for _, f := range flags {
		i := strings.Index(f, ":")
		if i < 0 {
			return nil, fmt.Errorf("--stack %q: expected NAME:PATH", f)
		}
		name, path := f[:i], f[i+1:]
		if name == "" || path == "" {
			return nil, fmt.Errorf("--stack %q: empty name or path", f)
		}
		refs = append(refs, StackRef{Name: name, Plan: path})
	}
	return refs, nil
}
