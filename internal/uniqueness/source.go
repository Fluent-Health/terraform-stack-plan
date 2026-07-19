package uniqueness

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"gopkg.in/yaml.v3"
)

// errNoEnvironments signals that a matched manifest carries no environments
// block at all (a single-env / non-comparable instance — e.g. a per-env-file
// BundleInstance with only top-level inputs). LoadUnits skips such a file
// rather than failing: there are no cross-env values in it to compare. It is
// NOT returned for a present-but-malformed environments block, which stays a
// hard error.
var errNoEnvironments = errors.New("uniqueness: manifest has no environments block")

// Catalyst defaults for a SourceBlock left unspecified — mirrors
// internal/config's defaults so LoadUnits behaves the same whether callers
// pass a fully-defaulted config.SourceBlock or a zero-value one directly.
const (
	defaultGlob             = "components/**/instances/*.tm.yml"
	defaultEnvironmentsPath = "environments"
	defaultInputsPath       = "inputs"
)

// LoadUnits discovers Catalyst instance manifests under root matching
// src.Glob, parses each as YAML, and builds one Unit per file.
//
// src.Glob has the "<prefix>/**/<suffix-glob>" shape (default
// "components/**/instances/*.tm.yml"); Go's filepath.Glob doesn't support
// "**", so this walks root with filepath.WalkDir instead, keeping any file
// whose root-relative path starts with the literal prefix directory and
// whose trailing path segments match the suffix glob (matched with
// filepath.Match against just the tail — so arbitrary directories may sit
// between the prefix and the suffix, honoring the "**").
//
// For each matched file: src.EnvironmentsPath (default "environments")
// navigates to a map keyed by environment name; for each environment,
// src.InputsPath (default "inputs") navigates to that environment's inputs,
// which are flattened (see Flatten) into Unit.Inputs[env]. Unit.ID is the
// file's root-relative path with the prefix and ".tm.yml" suffix trimmed
// (e.g. "components/x/api/instances/api.tm.yml" -> "x/api/instances/api").
// Unit.Envs is the sorted set of environment names found.
//
// Tier-leaf stripping (e.g. dropping a tier_class input before detection)
// is NOT this function's concern: any such field is parsed as an ordinary
// leaf, same as every other input. That's Evaluate's job.
//
// A matched file that can't be parsed, or whose environments block is present
// but malformed (not a map, bad inputs shape), is a hard error (fail loud). A
// matched file that lacks the environments block ENTIRELY is skipped, not an
// error — it's a single-env / non-comparable instance (e.g. a per-env-file
// BundleInstance with only top-level inputs) with no cross-env values to lint.
func LoadUnits(root string, src config.SourceBlock) ([]Unit, error) {
	glob := src.Glob
	if glob == "" {
		glob = defaultGlob
	}
	envPath := src.EnvironmentsPath
	if envPath == "" {
		envPath = defaultEnvironmentsPath
	}
	inputsPath := src.InputsPath
	if inputsPath == "" {
		inputsPath = defaultInputsPath
	}

	prefix, suffix, err := splitGlob(glob)
	if err != nil {
		return nil, err
	}

	paths, err := discoverPaths(root, prefix, suffix)
	if err != nil {
		return nil, err
	}

	units := make([]Unit, 0, len(paths))
	for _, rel := range paths {
		u, err := loadUnit(root, rel, prefix, envPath, inputsPath)
		if errors.Is(err, errNoEnvironments) {
			continue // non-comparable single-env instance; skip, don't fail
		}
		if err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, nil
}

// splitGlob splits a "<prefix>/**/<suffix-glob>" pattern into its literal
// prefix directory (with a trailing slash, so callers can strings.TrimPrefix
// a root-relative path directly) and its suffix glob.
func splitGlob(glob string) (prefix, suffixGlob string, err error) {
	parts := strings.SplitN(glob, "/**/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("env_uniqueness: source glob %q must have the <dir>/**/<pattern> shape", glob)
	}
	return parts[0] + "/", parts[1], nil
}

// discoverPaths walks root, returning the sorted, root-relative
// (slash-separated) paths of every regular file matching prefix/suffix.
func discoverPaths(root, prefix, suffixGlob string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		matched, err := matchGlob(rel, prefix, suffixGlob)
		if err != nil {
			return err
		}
		if matched {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("env_uniqueness: walking %s: %w", root, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// matchGlob reports whether a root-relative, slash-separated path starts
// with the literal prefix directory and whose trailing path segments (as
// many as suffixGlob has) match suffixGlob via filepath.Match. This is what
// lets the fixed prefix and suffix glob stand in for "**": any number of
// directories may sit between them.
func matchGlob(rel, prefix, suffixGlob string) (bool, error) {
	if !strings.HasPrefix(rel, prefix) {
		return false, nil
	}
	tail := strings.TrimPrefix(rel, prefix)

	tailParts := strings.Split(tail, "/")
	suffixParts := strings.Split(suffixGlob, "/")
	if len(tailParts) < len(suffixParts) {
		return false, nil
	}
	tailEnd := strings.Join(tailParts[len(tailParts)-len(suffixParts):], "/")

	return filepath.Match(suffixGlob, tailEnd)
}

// loadUnit parses one matched, root-relative file into a Unit.
func loadUnit(root, rel, prefix, envPath, inputsPath string) (Unit, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return Unit{}, fmt.Errorf("env_uniqueness: reading %s: %w", rel, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Unit{}, fmt.Errorf("env_uniqueness: parsing %s: %w", rel, err)
	}

	envsRaw, ok := navigate(doc, envPath)
	if !ok {
		// No environments block at all: skip (single-env / non-comparable
		// instance), don't fail. A malformed block below is still a hard error.
		return Unit{}, errNoEnvironments
	}
	envsMap, ok := envsRaw.(map[string]any)
	if !ok {
		return Unit{}, fmt.Errorf("env_uniqueness: %s: %q is not a map", rel, envPath)
	}

	inputs := make(map[string]map[string]any, len(envsMap))
	envNames := make([]string, 0, len(envsMap))
	for env, block := range envsMap {
		envNames = append(envNames, env)

		inputsMap, err := navigateInputs(block, inputsPath)
		if err != nil {
			return Unit{}, fmt.Errorf("env_uniqueness: %s: environment %q: %w", rel, env, err)
		}
		inputs[env] = Flatten(inputsMap)
	}
	sort.Strings(envNames)

	id := strings.TrimSuffix(strings.TrimPrefix(rel, prefix), ".tm.yml")

	return Unit{ID: id, Envs: envNames, Inputs: inputs}, nil
}

// navigateInputs resolves inputsPath within one environment's block (itself
// arbitrary YAML: a map, or nil for an empty "env: {}" block). A missing or
// nil inputs value is treated as empty inputs, not an error — an
// environment with no inputs at all is a legitimate, if unusual, shape.
func navigateInputs(envBlock any, inputsPath string) (map[string]any, error) {
	if envBlock == nil {
		return map[string]any{}, nil
	}
	blockMap, ok := envBlock.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("block is not a map (%T)", envBlock)
	}

	inputsRaw, ok := navigate(blockMap, inputsPath)
	if !ok || inputsRaw == nil {
		return map[string]any{}, nil
	}
	inputsMap, ok := inputsRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q is not a map (%T)", inputsPath, inputsRaw)
	}
	return inputsMap, nil
}

// navigate resolves a dot-separated path of map keys within doc, returning
// (nil, false) if any segment is missing or the value at any intermediate
// segment isn't itself a map.
func navigate(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
