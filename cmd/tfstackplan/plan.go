package main

import (
	"os"
	"path/filepath"
)

// gatherPlans collects each stack's tfplan.json (written by the terramate plan
// script in the stack dir, i.e. <root>/<stack>/tfplan.json) into a fresh temp
// plans-dir laid out as <plansDir>/<stack>/tfplan.json — the shape the render
// pipeline (--plans-dir) expects. Stacks without a tfplan.json (e.g. a plan
// failure) are skipped. The caller removes plansDir when done.
func gatherPlans(root string, stacks []string) (string, error) {
	plansDir, err := os.MkdirTemp("", "tfstackplan-plans-")
	if err != nil {
		return "", err
	}
	for _, s := range stacks {
		src := filepath.Join(root, filepath.FromSlash(s), "tfplan.json")
		data, err := os.ReadFile(src)
		if err != nil {
			continue // no plan for this stack (skipped/failed) — not fatal
		}
		dst := filepath.Join(plansDir, filepath.FromSlash(s))
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, "tfplan.json"), data, 0o644); err != nil {
			return "", err
		}
	}
	return plansDir, nil
}
