package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval/gcppam"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
)

func writePEM(t *testing.T) string {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildServeAppBootsHealthz(t *testing.T) {
	cfg := &config.Config{
		Serve: &config.ServeConfig{
			DBPath:        filepath.Join(t.TempDir(), "s.db"),
			PublicBaseURL: "https://srv",
			UseChecks:     true,
			GitHubApp:     &config.GitHubAppConfig{AppID: "1", InstallationID: "2", PrivateKeyPath: writePEM(t)},
			Approval:      &config.ApprovalConfig{Backend: "gcp-pam", RequesterPool: []string{"sa0"}},
		},
		Classes: []config.ClassBinding{{Name: "iam", Backend: "gcp-pam", Entitlement: "iam-elev", Required: true}},
	}
	fakeCreds := func(context.Context) (gcppam.TokenFunc, gcppam.ImpersonateFunc, error) {
		return func(context.Context) (string, error) { return "tok", nil },
			func(context.Context, string) (string, error) { return "imp", nil }, nil
	}
	app, cleanup, err := buildServeApp(context.Background(), cfg, "secret", fakeCreds)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
}

func TestBuildServeAppRequiresServeBlock(t *testing.T) {
	if _, _, err := buildServeApp(context.Background(), &config.Config{}, "", nil); err == nil {
		t.Error("want error when no serve block is configured")
	}
}

func TestGcppamConfigFromClasses(t *testing.T) {
	cfg := &config.Config{
		Serve:   &config.ServeConfig{Approval: &config.ApprovalConfig{Location: "us", Duration: "60s", RequesterPool: []string{"sa0"}}},
		Classes: []config.ClassBinding{{Name: "iam", Entitlement: "iam-elev"}, {Name: "database", Entitlement: "db"}},
	}
	gc := gcppamConfig(cfg)
	if gc.Location != "us" || gc.Duration != "60s" || len(gc.RequesterPool) != 1 {
		t.Errorf("gcppam config = %+v", gc)
	}
	if gc.Entitlements["iam"] != "iam-elev" || gc.Entitlements["database"] != "db" {
		t.Errorf("entitlements = %+v", gc.Entitlements)
	}
}
