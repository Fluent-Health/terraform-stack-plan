package main

import (
        "path/filepath"
        "testing"
)

func TestDispatchRoutesRunLint(t *testing.T) {
        if code := dispatch([]string{"run", "lint", "--dir", filepath.Join(t.TempDir(), "nope")}); code != 1 {
                t.Errorf("expected exit code 1 (stub failure), got %d", code)
        }
}
