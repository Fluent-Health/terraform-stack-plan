package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval"
	"github.com/Fluent-Health/terraform-stack-plan/internal/demo"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// TestServeE2E seeds a realistic scenario over the real HTTP surface and
// asserts the tier serve's RETAINED read surfaces. The tier no longer serves
// HTML — the central UI is the human surface — so this covers what remains:
// the execution JSON, the /img DAG (public: GitHub's camo proxy), the /plan
// fragment and /logs reads (OIDC-scoped in production; auth is disabled here
// with no APIVerifier), and the absence of the retired viewer routes.
func TestServeE2E(t *testing.T) {
	tempDir := t.TempDir()
	db, err := store.Open(filepath.Join(tempDir, "server.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	gh := &server.MockGitHub{}
	app := server.New(db, gh, server.Config{
		LogsDir: filepath.Join(tempDir, "logs"),
	})
	app.Approval = approval.NewFake()

	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	planID, applyID, err := demo.SeedScenario(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("seed scenario: %v", err)
	}

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// The execution read carries the full state for the central UI.
	if code, body := get("/api/execution/" + planID); code != 200 ||
		!strings.Contains(body, "apps/iam") || !strings.Contains(body, `"gates"`) {
		t.Errorf("execution read: %d %.200s", code, body)
	}

	// The DAG image stays public (GitHub's camo proxy cannot authenticate).
	if code, body := get("/img/" + applyID + ".svg"); code != 200 || !strings.Contains(body, "<svg") {
		t.Errorf("/img: %d %.100s", code, body)
	}

	// Plan fragment + log reads serve the central UI's proxies.
	if code, body := get("/plan/" + planID + "/apps/iam"); code != 200 || body == "" {
		t.Errorf("/plan fragment: %d %.100s", code, body)
	}

	// The retired viewer routes are gone.
	for _, path := range []string{"/", "/live/" + planID, "/pr/42", "/assets/app.css"} {
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("retired route %s = %d, want 404", path, code)
		}
	}
}
