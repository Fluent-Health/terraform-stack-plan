package runner

import "testing"

func TestClientFromEnv(t *testing.T) {
	t.Setenv(EnvServer, "https://srv/")
	t.Setenv(EnvToken, "s3cret")
	c := ClientFromEnv()
	if !c.Enabled() {
		t.Fatal("client should be enabled when TFSTACKPLAN_SERVER is set")
	}
	if c.baseURL != "https://srv" || c.secret != "s3cret" {
		t.Errorf("client = %q / %q", c.baseURL, c.secret)
	}
}

func TestClientFromEnvDisabledWhenUnset(t *testing.T) {
	t.Setenv(EnvServer, "")
	t.Setenv(EnvToken, "")
	if ClientFromEnv().Enabled() {
		t.Error("client should be disabled when TFSTACKPLAN_SERVER is empty")
	}
}
