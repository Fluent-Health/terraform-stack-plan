// Package source indexes a stack's Terraform source so a plan resource can be
// linked to the file:line where it is declared.
package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Loc is a resource declaration location, with File relative to the repo root.
type Loc struct {
	File string
	Line int
}

// Index maps (module, type, name) → declaration location.
type Index struct {
	m map[string]Loc
}

func key(moduleKey, typ, name string) string {
	return moduleKey + "\x00" + typ + "\x00" + name
}

// Build parses dir's *.tf (root module) plus any local modules listed in
// dir/.terraform/modules/modules.json, recording each resource block's
// location relative to repoRoot. Files outside repoRoot, unparseable files,
// and modules cached under .terraform are skipped. Build never fails: a missing
// or unreadable tree just yields a sparse index (callers fall back).
func Build(dir, repoRoot string) *Index {
	idx := &Index{m: map[string]Loc{}}
	idx.parseDir(dir, "", repoRoot)

	mjPath := filepath.Join(dir, ".terraform", "modules", "modules.json")
	if data, err := os.ReadFile(mjPath); err == nil {
		var doc struct {
			Modules []struct{ Key, Source, Dir string }
		}
		if json.Unmarshal(data, &doc) == nil {
			for _, m := range doc.Modules {
				if m.Key == "" || underDotTerraform(m.Dir) {
					continue
				}
				idx.parseDir(filepath.Join(dir, m.Dir), m.Key, repoRoot)
			}
		}
	}
	return idx
}

func (idx *Index) parseDir(dir, moduleKey, repoRoot string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		f, diags := hclsyntax.ParseConfig(src, full, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			continue
		}
		rel := relTo(repoRoot, full)
		if rel == "" {
			continue
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, blk := range body.Blocks {
			if blk.Type != "resource" || len(blk.Labels) != 2 {
				continue
			}
			k := key(moduleKey, blk.Labels[0], blk.Labels[1])
			if _, exists := idx.m[k]; exists {
				continue
			}
			idx.m[k] = Loc{File: rel, Line: blk.DefRange().Start.Line}
		}
	}
}

// Lookup resolves a plan resource. moduleAddress is the plan's module_address
// ("" for root, "module.a.module.b" for nested); the instance key is irrelevant.
func (idx *Index) Lookup(moduleAddress, typ, name string) (Loc, bool) {
	loc, ok := idx.m[key(moduleKey(moduleAddress), typ, name)]
	return loc, ok
}

// moduleKey turns a plan module_address ("module.a.module.b") into the
// modules.json key ("a.b"); root ("") stays "".
func moduleKey(moduleAddress string) string {
	if moduleAddress == "" {
		return ""
	}
	parts := strings.Split(moduleAddress, ".")
	var keys []string
	for i := 0; i < len(parts); i++ {
		if parts[i] == "module" && i+1 < len(parts) {
			keys = append(keys, parts[i+1])
			i++
		}
	}
	return strings.Join(keys, ".")
}

func underDotTerraform(dir string) bool {
	return dir == ".terraform" || strings.HasPrefix(dir, ".terraform/") || strings.Contains(dir, "/.terraform/")
}

// relTo returns target relative to root in slash form, or "" if target escapes root.
func relTo(root, target string) string {
	ra, err1 := filepath.Abs(root)
	ta, err2 := filepath.Abs(target)
	if err1 != nil || err2 != nil {
		return ""
	}
	rel, err := filepath.Rel(ra, ta)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}
