package e2e

import (
        "context"
        "io"
        "net/http"
        "net/http/httptest"
        "path/filepath"
        "strings"
        "testing"
        "time"

        "github.com/Fluent-Health/terraform-stack-plan/internal/demo"
        "github.com/Fluent-Health/terraform-stack-plan/internal/server"
        "github.com/Fluent-Health/terraform-stack-plan/internal/store"
        "github.com/Fluent-Health/terraform-stack-plan/internal/approval"
        "github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
)

func TestServeE2E(t *testing.T) {
        tempDir := t.TempDir()
        dbPath := filepath.Join(tempDir, "e2e.db")
        db, err := store.Open(dbPath)
        if err != nil {
                t.Fatalf("open store: %v", err)
        }
        defer db.Close()

        gh := &server.MockGitHub{}
        app := server.New(db, gh, server.Config{
                WebhookSecret: "e2e-secret",
                LogsDir:       filepath.Join(tempDir, "logs"),
        })
        app.Approval = approval.NewFake()

        srv := httptest.NewServer(app.Routes())
        defer srv.Close()

        // Mint a valid API token for seeding mutations
        apiToken, err := jwtutil.Make("e2e-secret", "runner", "api", time.Hour)
        if err != nil {
                t.Fatalf("mint api token: %v", err)
        }

        // Mint a valid View token for rendering GET routes
        viewToken, err := jwtutil.Make("e2e-secret", "viewer", "view", time.Hour)
        if err != nil {
                t.Fatalf("mint view token: %v", err)
        }

        // Seed the scenario (returns both plan and apply IDs)
        planID, applyID, err := demo.SeedScenario(context.Background(), srv.URL, apiToken)
        if err != nil {
                t.Fatalf("seed scenario: %v", err)
        }

        // 1. Assert GET / (index) loads successfully
        resp, err := http.Get(srv.URL + "/?token=" + viewToken)
        if err != nil {
                t.Fatalf("GET index: %v", err)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                t.Errorf("GET index status = %d, want 200", resp.StatusCode)
        }

        body, err := io.ReadAll(resp.Body)
        if err != nil {
                t.Fatalf("read index body: %v", err)
        }
        if !strings.Contains(string(body), "destructive&#43;iam") {
                t.Errorf("expected index page to show environment 'destructive+iam' (escaped as 'destructive&#43;iam')")
        }

        // 2. Assert GET /live/{id} renders the DAG SVG and the approval panel for apply ID
        resp2, err := http.Get(srv.URL + "/live/" + applyID + "?token=" + viewToken)
        if err != nil {
                t.Fatalf("GET live: %v", err)
        }
        defer resp2.Body.Close()
        if resp2.StatusCode != http.StatusOK {
                t.Errorf("GET live status = %d, want 200", resp2.StatusCode)
        }

        body2, err := io.ReadAll(resp2.Body)
        if err != nil {
                t.Fatalf("read live body: %v", err)
        }
        bodyStr := string(body2)

        if !strings.Contains(bodyStr, "apps/iam") || !strings.Contains(bodyStr, "apps/db") {
                t.Errorf("expected live page to contain gated stacks 'apps/iam' and 'apps/db'")
        }

        if !strings.Contains(bodyStr, "<svg") {
                t.Errorf("expected live page to render DAG SVG diagram")
        }

        if !strings.Contains(bodyStr, "Approvals") || !strings.Contains(bodyStr, "Awaiting approval") {
                t.Errorf("expected live page to contain the Approvals panel waiting for grant actions")
        }

        // 3. Assert GET /live/{id} renders successfully for plan ID too
        resp3, err := http.Get(srv.URL + "/live/" + planID + "?token=" + viewToken)
        if err != nil {
                t.Fatalf("GET live (plan): %v", err)
        }
        defer resp3.Body.Close()
        if resp3.StatusCode != http.StatusOK {
                t.Errorf("GET live (plan) status = %d, want 200", resp3.StatusCode)
        }
}
