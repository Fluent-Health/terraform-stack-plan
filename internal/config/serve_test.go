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
  backend           = "gcp-pam"
  entitlement       = "db-approval"
  entitlement_scope = "folders"
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
	if cfg.Classes[1].Name != "database" || cfg.Classes[1].Required || cfg.Classes[1].EntitlementScope != "folders" {
		t.Errorf("class database = %+v", cfg.Classes[1])
	}
}

func TestLoadServeBlock(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
serve {
  db_path            = "/data/server.db"
  public_base_url    = "https://srv.example"
  use_checks         = true
  webhook_secret_env = "WEBHOOK_SECRET"

  github_app {
    app_id           = "12345"
    installation_id  = "67890"
    private_key_path = "/secrets/app.pem"
  }

  approval "gcp-pam" {
    location       = "global"
    duration       = "28800s"
    requester_pool = ["sa0@x.iam.gserviceaccount.com", "sa1@x.iam.gserviceaccount.com"]
  }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Serve
	if s == nil {
		t.Fatal("serve block not parsed")
	}
	if s.DBPath != "/data/server.db" || s.PublicBaseURL != "https://srv.example" || !s.UseChecks || s.WebhookSecretEnv != "WEBHOOK_SECRET" {
		t.Errorf("serve = %+v", s)
	}
	if s.GitHubApp == nil || s.GitHubApp.AppID != "12345" || s.GitHubApp.InstallationID != "67890" || s.GitHubApp.PrivateKeyPath != "/secrets/app.pem" {
		t.Errorf("github_app = %+v", s.GitHubApp)
	}
	if s.Approval == nil || s.Approval.Backend != "gcp-pam" || s.Approval.Location != "global" || s.Approval.Duration != "28800s" || len(s.Approval.RequesterPool) != 2 {
		t.Errorf("approval = %+v", s.Approval)
	}
}

func TestLoadServeGroupBlock(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
serve {
  group {
    depth   = 3
    pattern = "^(x)"
  }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Serve == nil {
		t.Fatal("serve block not parsed")
	}
	if cfg.Serve.Group == nil || cfg.Serve.Group.Depth != 3 || cfg.Serve.Group.Pattern != "^(x)" {
		t.Errorf("serve.group = %+v, want depth 3 pattern ^(x)", cfg.Serve.Group)
	}
}

func TestLoadServeLogsAndObjects(t *testing.T) {
	cfg, err := Load(writeCfg(t, `
serve {
  db_path  = "x.db"
  logs_dir = "/var/log/tfsp"
  objects {
    backend = "gcs"
    bucket  = "my-bucket"
    prefix  = "logs"
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Serve.LogsDir != "/var/log/tfsp" {
		t.Errorf("logs_dir = %q", cfg.Serve.LogsDir)
	}
	if cfg.Serve.Objects == nil || cfg.Serve.Objects.Bucket != "my-bucket" || cfg.Serve.Objects.Prefix != "logs" || cfg.Serve.Objects.Backend != "gcs" {
		t.Errorf("objects = %+v", cfg.Serve.Objects)
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
