package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.hcl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadServerAndClassBlocks(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
server {
  url         = "https://srv.example"
  environment = "staging"
}
class "iam" {
  backend     = "gcp-pam"
  entitlement = "iam-elev"
  required    = true
}
class "database" {
  backend     = "gcp-pam"
  entitlement = "db-approval"
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server == nil || cfg.Server.URL != "https://srv.example" || cfg.Server.Environment != "staging" {
		t.Fatalf("server = %+v", cfg.Server)
	}
	if len(cfg.Classes) != 2 {
		t.Fatalf("classes = %d, want 2", len(cfg.Classes))
	}
	if cfg.Classes[0].Name != "iam" || cfg.Classes[0].Entitlement != "iam-elev" || !cfg.Classes[0].Required {
		t.Errorf("class iam = %+v", cfg.Classes[0])
	}
	if cfg.Classes[1].Name != "database" || cfg.Classes[1].Required {
		t.Errorf("class database = %+v", cfg.Classes[1])
	}
}

func TestLoadRenderOnlyConfigStillWorks(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != nil || cfg.Serve != nil || len(cfg.Classes) != 0 {
		t.Errorf("render-only config should have no serve blocks: %+v", cfg)
	}
	if cfg.Classification == nil {
		t.Error("classification should still parse")
	}
}
