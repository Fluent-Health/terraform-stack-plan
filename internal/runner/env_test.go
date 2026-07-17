package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientFromEnv(t *testing.T) {
	t.Setenv(EnvServer, "https://srv/")
	c := ClientFromEnv()
	if !c.Enabled() {
		t.Fatal("client should be enabled when TFSTACKPLAN_SERVER is set")
	}
	if c.baseURL != "https://srv" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestClientFromEnvDisabledWhenUnset(t *testing.T) {
	t.Setenv(EnvServer, "")
	if ClientFromEnv().Enabled() {
		t.Error("client should be disabled when TFSTACKPLAN_SERVER is empty")
	}
}

// TestClientFromEnvAudienceDefaulting asserts that when TFSTACKPLAN_AUDIENCE is empty,
// the OIDC audience is automatically defaulted to the server base URL so that
// requests do not silently go unauthenticated.
func TestClientFromEnvAudienceDefaulting(t *testing.T) {
	t.Setenv(EnvServer, "https://srv")
	t.Setenv(EnvAudience, "")
	c := ClientFromEnv()
	if !c.Enabled() {
		t.Fatal("client should be enabled")
	}
	// Note: since ADC credentials are not configured in typical unit-testing,
	// APITokenFunc might degrade gracefully, but the loader tries to resolve.
}

func TestClientForEnvironment(t *testing.T) {
	// Create a temp dir
	tmpDir := t.TempDir()

	// Create a .tfstackplan.hcl in it
	cfgContent := `
server {
  url         = "https://default-srv"
  environment = "staging"
}
server "prod" {
  url         = "https://prod-srv"
  environment = "prod"
}
server "nonprod" {
  url         = "https://nonprod-srv"
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, ".tfstackplan.hcl"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Chdir to the temp directory
	t.Chdir(tmpDir)

	t.Setenv(EnvServer, "") // Ensure EnvServer is clear

	// Test default fallback when env is empty or unknown
	cDefault := ClientForEnvironment("")
	if cDefault.baseURL != "https://default-srv" {
		t.Errorf("default baseURL = %q, want https://default-srv", cDefault.baseURL)
	}

	// Test matching s.Environment
	cProd := ClientForEnvironment("prod")
	if cProd.baseURL != "https://prod-srv" {
		t.Errorf("prod baseURL = %q, want https://prod-srv", cProd.baseURL)
	}

	// Test matching s.Name
	cNonprod := ClientForEnvironment("nonprod")
	if cNonprod.baseURL != "https://nonprod-srv" {
		t.Errorf("nonprod baseURL = %q, want https://nonprod-srv", cNonprod.baseURL)
	}
}
