package runner

import (
	"context"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
)

func TestClientFromEnv(t *testing.T) {
	t.Setenv(EnvServer, "https://srv/")
	t.Setenv(EnvToken, "s3cret")
	c := ClientFromEnv()
	if !c.Enabled() {
		t.Fatal("client should be enabled when TFSTACKPLAN_SERVER is set")
	}
	if c.baseURL != "https://srv" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	// The shared secret must mint legacy HS256 api tokens.
	tok, err := c.token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwtutil.Validate(tok, "s3cret", "api"); err != nil {
		t.Errorf("token from env secret invalid: %v", err)
	}
}

func TestClientFromEnvDisabledWhenUnset(t *testing.T) {
	t.Setenv(EnvServer, "")
	t.Setenv(EnvToken, "")
	if ClientFromEnv().Enabled() {
		t.Error("client should be disabled when TFSTACKPLAN_SERVER is empty")
	}
}
