package e2e

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/demo"
	"github.com/Fluent-Health/terraform-stack-plan/internal/server"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestCLIE2E(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "e2e.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// 1. Compile the tfstackplan binary
	binaryPath := filepath.Join(tempDir, "tfstackplan")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "../cmd/tfstackplan")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v. Output:\n%s", err, string(out))
	}

	gh := &server.MockGitHub{}
	app := server.New(db, gh, server.Config{
		LogsDir: filepath.Join(tempDir, "logs"),
	})

	srv := httptest.NewServer(app.Routes())
	defer srv.Close()

	// Seed the scenario (returns both plan and apply IDs)
	planID, _, err := demo.SeedScenario(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("seed scenario: %v", err)
	}

	// 2. Test "tfstackplan state import"
	stackDir := filepath.Join(tempDir, "stacks/a")
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdImport := exec.Command(binaryPath, "state", "import", "--dir", tempDir, "--stack", "stacks/a", "--pr", "15", "aws_s3_bucket.main", "bucket-15")
	if err := cmdImport.Run(); err != nil {
		t.Fatalf("tfstackplan state import failed: %v", err)
	}
	// Verify shim file exists and contains correct content
	shimPath := filepath.Join(stackDir, "_tfsp_move.PR-15.tf")
	data, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "import {\n  to = aws_s3_bucket.main\n  id = \"bucket-15\"\n}") {
		t.Errorf("expected import block, got:\n%s", string(data))
	}

	// 3. Test "tfstackplan state remove"
	cmdRemove := exec.Command(binaryPath, "state", "remove", "--dir", tempDir, "--stack", "stacks/a", "--pr", "15", "aws_s3_bucket.main")
	if err := cmdRemove.Run(); err != nil {
		t.Fatalf("tfstackplan state remove failed: %v", err)
	}
	data2, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data2), "removed {\n  from = aws_s3_bucket.main") {
		t.Errorf("expected removed block, got:\n%s", string(data2))
	}

	// 4. Test "tfstackplan run status" (JSON output)
	cmdStatus := exec.Command(binaryPath, "run", "status", "--server", srv.URL, "--format", "json", planID)
	out, err := cmdStatus.CombinedOutput()
	if err != nil {
		t.Fatalf("tfstackplan run status failed: %v. Output:\n%s", err, string(out))
	}

	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("failed to parse json output: %v. Output:\n%s", err, string(out))
	}
	if res["ID"] != planID {
		t.Errorf("expected execution ID %q, got %v", planID, res["ID"])
	}
}
