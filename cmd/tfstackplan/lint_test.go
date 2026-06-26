package main

import (
	"path/filepath"
	"testing"
)

func TestDispatchRoutesRunLint(t *testing.T) {
	if code := dispatch([]string{"run", "lint", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 0 {
		t.Errorf("expected exit code 0 (for now), got %d", code)
	}
}

func TestRunLintRequiresDir(t *testing.T) {
	if code := runLint([]string{}); code != 2 {
		t.Errorf("expected exit code 2 when --dir is missing, got %d", code)
	}
}
