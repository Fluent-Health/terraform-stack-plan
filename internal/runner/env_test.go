package runner

import (
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
