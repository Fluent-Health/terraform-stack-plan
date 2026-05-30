package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAML(t *testing.T) {
	m, err := Load("testdata/plan.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Terraform plan — nonprod" || m.Marker != "tfstackplan:nonprod" {
		t.Fatalf("bad header: %+v", m)
	}
	if len(m.Stacks) != 2 || m.Stacks[0].Name != "platform/nonprod" ||
		m.Stacks[1].Plan != "./out/app-dev/plan.json" {
		t.Fatalf("bad stacks: %+v", m.Stacks)
	}
}

func TestParseStackFlags(t *testing.T) {
	refs, err := ParseStackFlags([]string{
		"platform/nonprod:./out/a/plan.json",
		"svc:/abs/path/plan.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].Name != "platform/nonprod" || refs[0].Plan != "./out/a/plan.json" {
		t.Fatalf("bad ref 0: %+v", refs[0])
	}
	if refs[1].Name != "svc" || refs[1].Plan != "/abs/path/plan.json" {
		t.Fatalf("bad ref 1: %+v", refs[1])
	}
}

func TestParseStackFlagInvalid(t *testing.T) {
	if _, err := ParseStackFlags([]string{"noseparator"}); err == nil {
		t.Fatal("expected error for missing ':' separator")
	}
}

func TestLoadEmptyStacksIsValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(p, []byte("title: \"T\"\nmarker: \"m\"\nstacks: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(p)
	if err != nil {
		t.Fatalf("empty manifest should load, got error: %v", err)
	}
	if len(m.Stacks) != 0 {
		t.Fatalf("want 0 stacks, got %d", len(m.Stacks))
	}
	if m.Title != "T" || m.Marker != "m" {
		t.Fatalf("title/marker not parsed: %+v", m)
	}
}
