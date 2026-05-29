package config

import (
	"strings"
	"testing"
)

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
