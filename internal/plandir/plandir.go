// Package plandir discovers per-stack Terraform plan JSON files under a single
// directory. Each `tfplan.json` found defines one stack; the stack's name is the
// directory containing it, relative to the scanned root (forward-slash form).
package plandir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// PlanFile is the fixed filename Scan looks for in each stack directory.
// It matches Terragrunt's `--json-out-dir` output; Terramate is scripted to
// write the same name.
const PlanFile = "tfplan.json"

// Stack is one discovered stack: its name and the path to its plan JSON.
type Stack struct {
	Name string // dir containing the plan, relative to the scan root (forward-slash)
	Plan string // filesystem path to the tfplan.json
}

// Scan walks dir and returns one Stack per tfplan.json found, sorted
// lexicographically by Name. A nonexistent dir is an error; an existing dir
// with no plan files returns an empty slice and no error.
func Scan(dir string) ([]Stack, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plans-dir %q is not a directory", dir)
	}

	var stacks []Stack
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != PlanFile {
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(path))
		if err != nil {
			return err
		}
		stacks = append(stacks, Stack{Name: filepath.ToSlash(rel), Plan: path})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, nil
}
