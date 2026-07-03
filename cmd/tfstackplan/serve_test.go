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
	"strings"
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

func TestBuildServeAppBootsReady(t *testing.T) {
	cfg := &config.Config{
		Serve: &config.ServeConfig{
			DBPath:        filepath.Join(t.TempDir(), "s.db"),
			PublicBaseURL: "https://srv",
			GitHubApp:     &config.GitHubAppConfig{AppID: "1", InstallationID: "2", PrivateKeyPath: writePEM(t)},
			Approval:      &config.ApprovalConfig{Backend: "gcp-pam", RequesterPool: []string{"sa0"}},
		},
		Classes: []config.ClassBinding{{Name: "iam", Backend: "gcp-pam", Entitlement: "iam-elev", Required: true}},
	}
	fakeCreds := func(context.Context) (gcppam.TokenFunc, gcppam.ImpersonateFunc, error) {
		return func(context.Context) (string, error) { return "tok", nil },
			func(context.Context, string) (string, error) { return "imp", nil }, nil
	}
	app, cleanup, err := buildServeApp(context.Background(), cfg, "secret", "", fakeCreds)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ready = %d", resp.StatusCode)
	}
}

func TestAPIPrincipals(t *testing.T) {
	if got := apiPrincipals(&config.ServeConfig{}); got != nil {
		t.Errorf("no api_auth block → nil, got %v", got)
	}
	m := apiPrincipals(&config.ServeConfig{APIAuth: &config.APIAuthConfig{Principals: []config.APIAuthPrincipal{
		{Email: "Runner@X.iam.gserviceaccount.com", Scopes: []string{"report"}},
		{Email: "ops@example.com", Scopes: []string{"read", "admin"}},
	}}})
	if got := m["runner@x.iam.gserviceaccount.com"]; len(got) != 1 || got[0] != "report" {
		t.Errorf("emails must be lowercased in the map: %v", m)
	}
	if got := m["ops@example.com"]; len(got) != 2 {
		t.Errorf("ops scopes = %v", got)
	}
}

// TestBuildServeAppAPIAuthNeedsAudience: api_auth with neither an audience nor
// a public_base_url must fail at startup, not silently reject every caller.
func TestBuildServeAppAPIAuthNeedsAudience(t *testing.T) {
	cfg := &config.Config{
		Serve: &config.ServeConfig{
			DBPath:    filepath.Join(t.TempDir(), "s.db"),
			GitHubApp: &config.GitHubAppConfig{AppID: "1", InstallationID: "2", PrivateKeyPath: writePEM(t)},
			APIAuth:   &config.APIAuthConfig{Principals: []config.APIAuthPrincipal{{Email: "a@b.c", Scopes: []string{"report"}}}},
		},
	}
	if _, _, err := buildServeApp(context.Background(), cfg, "", "", nil); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("want audience startup error, got %v", err)
	}
}

// TestBuildServeAppWiresAPIVerifier: an api_auth block must arm OIDC-only auth
// (no shared secret): unauthenticated /api/* calls are rejected.
func TestBuildServeAppWiresAPIVerifier(t *testing.T) {
	cfg := &config.Config{
		Serve: &config.ServeConfig{
			DBPath:        filepath.Join(t.TempDir(), "s.db"),
			PublicBaseURL: "https://srv",
			GitHubApp:     &config.GitHubAppConfig{AppID: "1", InstallationID: "2", PrivateKeyPath: writePEM(t)},
			APIAuth:       &config.APIAuthConfig{Principals: []config.APIAuthPrincipal{{Email: "a@b.c", Scopes: []string{"report"}}}},
		},
	}
	app, cleanup, err := buildServeApp(context.Background(), cfg, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if app.APIVerifier == nil {
		t.Fatal("APIVerifier should be wired from the api_auth block")
	}
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/init", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/init with api_auth armed = %d, want 401", resp.StatusCode)
	}
}

func TestBuildServeAppRequiresServeBlock(t *testing.T) {
	if _, _, err := buildServeApp(context.Background(), &config.Config{}, "", "", nil); err == nil {
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

func TestGcppamConfigPerClassScope(t *testing.T) {
	cfg := &config.Config{
		Classes: []config.ClassBinding{
			{Name: "iam", Entitlement: "iam-ent"},
			{Name: "database", Entitlement: "db-ent", EntitlementScope: "folders"},
		},
		Serve: &config.ServeConfig{},
	}
	gc := gcppamConfig(cfg)
	if gc.Entitlements["iam"] != "iam-ent" || gc.Entitlements["database"] != "db-ent" {
		t.Fatalf("entitlements = %+v", gc.Entitlements)
	}
	if gc.EntitlementScopes["database"] != "folders" {
		t.Errorf("database scope = %q, want folders", gc.EntitlementScopes["database"])
	}
	if _, ok := gc.EntitlementScopes["iam"]; ok {
		t.Errorf("iam should have no explicit scope (defaults to projects)")
	}
}

func TestDefaultLogsDir(t *testing.T) {
	if got := defaultLogsDir("/explicit/logs", "/data/tfstackplan.db"); got != "/explicit/logs" {
		t.Fatalf("explicit logs_dir overridden: %q", got)
	}
	if got := defaultLogsDir("", "/data/tfstackplan.db"); got != filepath.Join("/data", "logs") {
		t.Fatalf("default = %q, want /data/logs", got)
	}
}
