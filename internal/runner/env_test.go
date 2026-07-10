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

// TestClientFromEnvUnauthenticatedWithoutCredentials: with the audience unset,
// the client must stay unauthenticated — OIDC is opt-in via
// TFSTACKPLAN_AUDIENCE, so ambient machine credentials are never probed.
func TestClientFromEnvUnauthenticatedWithoutCredentials(t *testing.T) {
	t.Setenv(EnvServer, "https://srv")
	t.Setenv(EnvAudience, "")
	c := ClientFromEnv()
	if !c.Enabled() {
		t.Fatal("client should be enabled")
	}
	if c.token != nil {
		t.Error("token source should be nil without TFSTACKPLAN_AUDIENCE")
	}
}
