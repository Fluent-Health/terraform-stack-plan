package plandir

import (
	"os"
	"path/filepath"
	"testing"
)

// write creates <root>/<name>/tfplan.json (name may contain forward slashes).
func write(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name), "tfplan.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanNamesAndSorts(t *testing.T) {
	dir := t.TempDir()
	// Written out of order and at varying depth.
	pPlatform := write(t, dir, "platform/nonprod")
	write(t, dir, "data/warehouse")
	write(t, dir, "service-projects/app-dev")

	got, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"data/warehouse", "platform/nonprod", "service-projects/app-dev"}
	if len(got) != len(wantNames) {
		t.Fatalf("got %d stacks, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, w := range wantNames {
		if got[i].Name != w {
			t.Errorf("stack[%d].Name = %q, want %q", i, got[i].Name, w)
		}
	}
	// Plan path points at the actual file (forward-slash name → real path).
	if got[1].Plan != pPlatform {
		t.Errorf("platform/nonprod Plan = %q, want %q", got[1].Plan, pPlatform)
	}
}

func TestScanIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackA")
	// A stray non-plan file in another subdir must not become a stack.
	other := filepath.Join(dir, "stackB", "plan.json") // wrong filename
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "stackA" {
		t.Fatalf("expected only stackA, got %+v", got)
	}
}

func TestScanEmptyDirNoError(t *testing.T) {
	dir := t.TempDir() // exists, no tfplan.json
	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 stacks, got %+v", got)
	}
}

func TestScanMissingDirErrors(t *testing.T) {
	_, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestScanNonDirErrors(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(f); err == nil {
		t.Fatal("expected error when path is a file, not a directory")
	}
}
