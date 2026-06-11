package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

func TestRunPlanRequiresDir(t *testing.T) {
	if code := runPlan([]string{}); code == 0 {
		t.Error("run plan with no --dir should error")
	}
}

func TestDispatchRoutesRunPlan(t *testing.T) {
	if code := dispatch([]string{"run", "plan", "--dir", filepath.Join(t.TempDir(), "nope")}); code == 0 {
		t.Error("run plan on a missing dir should be non-zero")
	}
}

func TestGatherPlans(t *testing.T) {
	root := t.TempDir()
	for _, s := range []string{"stacks/a", "stacks/b"} {
		dir := filepath.Join(root, s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tfplan.json"), []byte(`{"format_version":"1.2"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plansDir, err := gatherPlans(root, []string{"stacks/a", "stacks/b", "stacks/c"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(plansDir) })
	for _, s := range []string{"stacks/a", "stacks/b"} {
		if _, err := os.Stat(filepath.Join(plansDir, s, "tfplan.json")); err != nil {
			t.Errorf("missing gathered plan for %s: %v", s, err)
		}
	}
	if _, err := os.Stat(filepath.Join(plansDir, "stacks/c", "tfplan.json")); err == nil {
		t.Error("stacks/c had no plan; should not be gathered")
	}
}

func TestRunPlanE2E(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/planfixture")); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("terramate", "version")
	probe.Dir = dir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("terramate not runnable: %v: %s", err, out)
	}
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		c := exec.Command("git", a...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", a, err, out)
		}
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	planJSON := `{"format_version":"1.2","resource_changes":[{"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x","change":{"actions":["create"],"before":null,"after":{"project":"proj-a"}}}]}`
	stub := "#!/bin/sh\ncase \"$1 $2\" in\n  \"show -json\") cat <<'J'\n" + planJSON + "\nJ\n  ;;\n  *) : ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var mu sync.Mutex
	var gotInit events.Init
	var gotFinal events.Finalize
	logs := map[string]string{} // stack → concatenated data
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		switch r.URL.Path {
		case "/api/init":
			_ = json.Unmarshal(b, &gotInit)
		case "/api/finalize":
			_ = json.Unmarshal(b, &gotFinal)
		case "/api/logs":
			var lc events.LogChunk
			_ = json.Unmarshal(b, &lc)
			logs[lc.Stack] += lc.Data
		}
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()
	t.Setenv(runner.EnvServer, srv.URL)
	t.Setenv(runner.EnvEnvironment, "staging")
	t.Setenv(runner.EnvExecution, "")

	if code := runPlan([]string{"--dir", dir, "--changed=false"}); code != 0 {
		t.Fatalf("run plan = %d, want 0", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotInit.Stacks) != 2 {
		t.Errorf("init stacks = %d, want 2", len(gotInit.Stacks))
	}
	if gotFinal.ReportMarkdown == "" {
		t.Error("finalize report empty")
	}
	found := false
	for _, g := range gotFinal.Gates {
		if g.Class == "iam" && g.Target == "proj-a" {
			found = true
		}
	}
	if !found {
		t.Errorf("finalize gates = %+v, want iam/proj-a", gotFinal.Gates)
	}
	if len(gotFinal.StackReports) == 0 {
		t.Error("finalize carried no per-stack reports")
	}
	if len(gotFinal.Categories) == 0 {
		t.Error("finalize carried no per-stack categories")
	}
	for _, s := range []string{"stacks/a", "stacks/b"} {
		if !strings.Contains(logs[s], s) {
			t.Errorf("logs[%q] = %q, want it to contain %q", s, logs[s], s)
		}
	}
}
