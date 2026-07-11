package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadUIConfig(t *testing.T, hcl string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ui.hcl")
	if err := os.WriteFile(p, []byte(hcl), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestUIConfigParses(t *testing.T) {
	cfg, err := loadUIConfig(t, `
ui {
  public_base_url    = "https://ui.example.com/"
  session_secret_env = "TFSTACKPLAN_UI_SESSION_SECRET"
  tier "nonprod" { url = "https://nonprod.example.com/" }
  tier "prod" {
    url      = "https://prod.example.com"
    audience = "https://prod-audience.example.com"
  }
  oauth {
    client_id         = "1234.apps.googleusercontent.com"
    client_secret_env = "TFSTACKPLAN_UI_OAUTH_SECRET"
    allowed_domain    = "Example.COM"
  }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	u := cfg.UI
	if u == nil {
		t.Fatal("ui block not parsed")
	}
	if u.PublicBaseURL != "https://ui.example.com" {
		t.Errorf("public_base_url not trimmed: %q", u.PublicBaseURL)
	}
	if u.SessionSecretEnv != "TFSTACKPLAN_UI_SESSION_SECRET" {
		t.Errorf("session_secret_env: %q", u.SessionSecretEnv)
	}
	if len(u.Tiers) != 2 {
		t.Fatalf("tiers: %+v", u.Tiers)
	}
	if u.Tiers[0].Name != "nonprod" || u.Tiers[0].URL != "https://nonprod.example.com" {
		t.Errorf("tier[0]: %+v", u.Tiers[0])
	}
	if u.Tiers[0].Audience != "https://nonprod.example.com" {
		t.Errorf("audience should default to url: %+v", u.Tiers[0])
	}
	if u.Tiers[1].Audience != "https://prod-audience.example.com" {
		t.Errorf("explicit audience should win: %+v", u.Tiers[1])
	}
	if u.OAuth == nil || u.OAuth.ClientID != "1234.apps.googleusercontent.com" {
		t.Fatalf("oauth: %+v", u.OAuth)
	}
	if u.OAuth.AllowedDomain != "example.com" {
		t.Errorf("allowed_domain should lowercase: %q", u.OAuth.AllowedDomain)
	}
}

func TestUIConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		hcl  string
		want string
	}{
		{"no tiers", "ui {\n}", "at least one tier"},
		{"missing url", "ui {\n  tier \"a\" {\n  }\n}", "url is required"},
		{"duplicate tier", "ui {\n  tier \"a\" {\n    url = \"https://x\"\n  }\n  tier \"a\" {\n    url = \"https://y\"\n  }\n}", "duplicate tier"},
		{"oauth missing client", "ui {\n  tier \"a\" {\n    url = \"https://x\"\n  }\n  oauth {\n    allowed_domain = \"x.com\"\n  }\n}", "client_id and client_secret_env"},
		{"oauth missing domain", "ui {\n  tier \"a\" {\n    url = \"https://x\"\n  }\n  oauth {\n    client_id = \"c\"\n    client_secret_env = \"E\"\n  }\n}", "allowed_domain is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadUIConfig(t, c.hcl)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestExampleUIConfigParses(t *testing.T) {
	cfg, err := Load("../../examples/ui.tfstackplan.hcl")
	if err != nil {
		t.Fatalf("examples/ui.tfstackplan.hcl must parse: %v", err)
	}
	if cfg.UI == nil {
		t.Fatalf("ui block not parsed: %+v", cfg)
	}
	if len(cfg.UI.Tiers) < 2 || cfg.UI.OAuth == nil {
		t.Errorf("example should show two tiers and oauth: %+v", cfg.UI)
	}
}
