package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverWalksUpToRepoRoot: a config at the repo root is found from a nested
// subdir (e.g. `run apply --dir stacks/<tier>` discovers the root .tfstackplan.hcl),
// but the search stops at the repo root (.git) so a config above the repo is not.
func TestDiscoverWalksUpToRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, DefaultFilename)
	if err := os.WriteFile(cfg, []byte("classification {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "stacks", "prod")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// From a nested subdir: walks up and finds the repo-root config.
	got, ok := Discover(sub)
	if !ok {
		t.Fatalf("Discover(%q) = not found; want the repo-root config", sub)
	}
	if got, want := mustAbs(t, got), mustAbs(t, cfg); got != want {
		t.Errorf("Discover found %q, want %q", got, want)
	}

	// A config ABOVE the repo root (outside .git) must NOT be discovered.
	above := t.TempDir()
	if err := os.WriteFile(filepath.Join(above, DefaultFilename), []byte("classification {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(above, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := Discover(deep); ok {
		t.Errorf("Discover(%q) found a config above the repo root; want none (stop at .git)", deep)
	}
}

// TestExampleClientConfigParses guards the canonical client example: it must stay
// valid HCL (classification + gating class + diff + links + progress).
func TestExampleClientConfigParses(t *testing.T) {
	cfg, err := Load("../../examples/.tfstackplan.hcl")
	if err != nil {
		t.Fatalf("examples/.tfstackplan.hcl must parse: %v", err)
	}
	if cfg.Classification == nil {
		t.Error("example should define a classification block")
	}
	if len(cfg.Classes) == 0 {
		t.Error("example should define a gating class")
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLoadFullOrdered(t *testing.T) {
	c, err := Load("testdata/full.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if c.Classification == nil {
		t.Fatal("expected classification block")
	}
	if c.Classification.Default.Name != "safe" || c.Classification.Default.Icon != "✅" {
		t.Fatalf("bad default: %+v", c.Classification.Default)
	}
	got := []string{}
	for _, r := range c.Classification.Rules {
		got = append(got, r.Name)
	}
	if strings.Join(got, ",") != "iam,destructive" {
		t.Fatalf("rule order = %v, want [iam destructive]", got)
	}
	if c.Classification.Rules[0].Icon != "⚠️" {
		t.Fatalf("iam icon override = %q, want ⚠️", c.Classification.Rules[0].Icon)
	}
	if c.Diff.MaxAttributeLines != 200 || !c.Diff.Detect {
		t.Fatalf("bad diff config: %+v", c.Diff)
	}
	if len(c.Diff.Overrides) != 1 || c.Diff.Overrides[0].Differ != "yaml" {
		t.Fatalf("bad diff overrides: %+v", c.Diff.Overrides)
	}
}

func TestLoadShorthandDefault(t *testing.T) {
	c, err := Load("testdata/shorthand.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if c.Classification.Default.Name != "safe" || c.Classification.Default.Icon != "" {
		t.Fatalf("shorthand default = %+v, want {safe }", c.Classification.Default)
	}
	if len(c.Classification.Rules) != 1 || c.Classification.Rules[0].Name != "iam" {
		t.Fatalf("expected iam preset rule, got %+v", c.Classification.Rules)
	}
}

func TestLoadPresetWithDerive(t *testing.T) {
	c, err := Load("testdata/derive.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Classification.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(c.Classification.Rules))
	}
	r := c.Classification.Rules[0]
	if len(r.Derivations) != 1 {
		t.Fatalf("expected 1 derivation, got %d", len(r.Derivations))
	}
	d := r.Derivations[0]
	if d.Attribute != "project" || d.FromAttribute != "bucket" {
		t.Fatalf("derivation = %+v, want attribute=project from=bucket", d)
	}
	if d.TypePattern == nil || !d.TypePattern.MatchString("google_storage_managed_folder_iam_member") {
		t.Fatalf("type pattern did not match managed_folder member")
	}
	if d.Pattern == nil {
		t.Fatal("nil pattern")
	}
	m := d.Pattern.FindStringSubmatch("fh-dev-svc-build-cache")
	if m == nil || m[d.Pattern.SubexpIndex("value")] != "fh-dev-svc" {
		t.Fatalf("pattern capture = %v, want fh-dev-svc", m)
	}
}

func TestDeriveBadPatternFails(t *testing.T) {
	_, err := Load("testdata/derive_badpattern.hcl")
	if err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("expected bad-pattern error, got %v", err)
	}
}

func TestUnknownPresetFails(t *testing.T) {
	_, err := Load("testdata/badpreset.hcl")
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("expected unknown-preset error, got %v", err)
	}
}

func TestDiffResolve(t *testing.T) {
	c, err := Load("testdata/full.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Diff.Resolve("kubernetes_manifest", "manifest"); got != "yaml" {
		t.Fatalf("Resolve(match) = %q, want yaml", got)
	}
	if got := c.Diff.Resolve("google_storage_bucket", "content"); got != "" {
		t.Fatalf("Resolve(no match) = %q, want empty", got)
	}
	// matching type but non-matching attribute → no override
	if got := c.Diff.Resolve("kubernetes_manifest", "other_attr"); got != "" {
		t.Fatalf("Resolve(type match, attr mismatch) = %q, want empty", got)
	}
}

func TestBadRegexFails(t *testing.T) {
	_, err := Load("testdata/badregex.hcl")
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestUnknownTopLevelBlockFails(t *testing.T) {
	_, err := Load("testdata/unknownblock.hcl")
	if err == nil {
		t.Fatal("expected error for unknown top-level block")
	}
}

func TestLoadLinks(t *testing.T) {
	cfg, err := Load("testdata/links.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Links == nil {
		t.Fatal("expected Links to be parsed")
	}
	if cfg.Links.Resource != "https://gh/o/r/blob/{sha}/{file}#L{line}" {
		t.Errorf("resource template = %q", cfg.Links.Resource)
	}
	if cfg.Links.Stack != "https://gh/o/r/tree/{sha}/{stack_dir}" {
		t.Errorf("stack template = %q", cfg.Links.Stack)
	}
	if len(cfg.Links.Header) != 2 || cfg.Links.Header[0].Label != "Build #{build_id}" || cfg.Links.Header[1].URL != "https://gh/o/r/pull/{pr}" {
		t.Errorf("header = %+v", cfg.Links.Header)
	}
}

func TestLoadEmitAttributes(t *testing.T) {
	c, err := Load("testdata/emit.hcl")
	if err != nil {
		t.Fatal(err)
	}
	rules := c.Classification.Rules
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}
	// preset "iam" carries emit_attributes
	if len(rules[0].EmitAttributes) != 1 || rules[0].EmitAttributes[0] != "project" {
		t.Fatalf("iam EmitAttributes = %v, want [project]", rules[0].EmitAttributes)
	}
	// custom rule "destructive" carries emit_attributes
	if len(rules[1].EmitAttributes) != 2 || rules[1].EmitAttributes[0] != "name" || rules[1].EmitAttributes[1] != "id" {
		t.Fatalf("destructive EmitAttributes = %v, want [name id]", rules[1].EmitAttributes)
	}
}
