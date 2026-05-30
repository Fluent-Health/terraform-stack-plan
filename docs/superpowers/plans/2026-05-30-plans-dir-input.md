# `--plans-dir` Input Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace tfstackplan's `--manifest`/`--stack` inputs with a single convention-based directory scan: `tfstackplan --plans-dir out/`, where `out/<stack>/tfplan.json` holds each stack's plan.

**Architecture:** A new `internal/plandir` package recursively scans a directory for `tfplan.json` files; each file's parent dir (relative to the scan root) is the stack name. `cmd/tfstackplan` drops its old input branches and the `internal/manifest` package entirely, deriving each stack's source dir as `join(--repo-root, name)` so source-aware links keep working on a mirrored tree. Title/marker move to flags. Everything downstream (classify, diff, links, sidecar, `fit`, render) is unchanged.

**Tech Stack:** Go 1.23, standard library only (`path/filepath`, `io/fs`, `sort`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-30-plans-dir-input-design.md`. **Background:** `docs/terramate-integration.md`.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/plandir/plandir.go` (new) | `Scan(dir)` → sorted `[]Stack{Name, Plan}`; the only discovery logic. |
| `internal/plandir/plandir_test.go` (new) | Unit tests for scanning, naming, ordering, errors. |
| `cmd/tfstackplan/main.go` (modify) | `--plans-dir` flag; `run` calls `plandir.Scan`; source dir = `join(repoRoot, name)`; drops manifest/stack handling. |
| `cmd/tfstackplan/main_test.go` (rewrite) | e2e tests on the new input mode. |
| `cmd/tfstackplan/examples_test.go` (modify) | Helpers write the `out/<name>/tfplan.json` tree and pass `plansDir`. |
| `internal/manifest/` (delete) | Removed entirely. |
| `examples/manifest.yaml` (delete) | Removed (manifest input gone). |
| `README.md`, `docs/DESIGN.md` (modify) | Rewrite input docs; add orchestrator recipes. |

**Note on golden examples:** `plandir.Scan` sorts stacks lexicographically, so the regenerated `examples/*.md` will list stacks in a new (sorted) order. This is expected — the example assertions check rendering invariants, not stack order, so they still pass. The hand-written "What it looks like" table in `README.md` is illustrative prose and is edited separately, not regenerated.

---

### Task 1: `internal/plandir` package

**Files:**
- Create: `internal/plandir/plandir.go`
- Test: `internal/plandir/plandir_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/plandir/plandir_test.go`:

```go
package plandir

import (
	"os"
	"path/filepath"
	"testing"
)

// write creates <root>/<name>/tfplan.json (name may contain forward slashes).
func write(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name), "tfplan.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanNamesAndSorts(t *testing.T) {
	dir := t.TempDir()
	// Written out of order and at varying depth.
	pPlatform := write(t, dir, "platform/nonprod")
	write(t, dir, "data/warehouse")
	write(t, dir, "service-projects/app-dev")

	got, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"data/warehouse", "platform/nonprod", "service-projects/app-dev"}
	if len(got) != len(wantNames) {
		t.Fatalf("got %d stacks, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, w := range wantNames {
		if got[i].Name != w {
			t.Errorf("stack[%d].Name = %q, want %q", i, got[i].Name, w)
		}
	}
	// Plan path points at the actual file (forward-slash name → real path).
	if got[1].Plan != pPlatform {
		t.Errorf("platform/nonprod Plan = %q, want %q", got[1].Plan, pPlatform)
	}
}

func TestScanIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stackA")
	// A stray non-plan file in another subdir must not become a stack.
	other := filepath.Join(dir, "stackB", "plan.json") // wrong filename
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "stackA" {
		t.Fatalf("expected only stackA, got %+v", got)
	}
}

func TestScanEmptyDirNoError(t *testing.T) {
	dir := t.TempDir() // exists, no tfplan.json
	got, err := Scan(dir)
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 stacks, got %+v", got)
	}
}

func TestScanMissingDirErrors(t *testing.T) {
	_, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/plandir/`
Expected: FAIL — `undefined: Scan` (package has no implementation yet).

- [ ] **Step 3: Implement `Scan`**

Create `internal/plandir/plandir.go`:

```go
// Package plandir discovers per-stack Terraform plan JSON files under a single
// directory. Each `tfplan.json` found defines one stack; the stack's name is the
// directory containing it, relative to the scanned root (forward-slash form).
package plandir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// PlanFile is the fixed filename Scan looks for in each stack directory.
// It matches Terragrunt's `--json-out-dir` output; Terramate is scripted to
// write the same name.
const PlanFile = "tfplan.json"

// Stack is one discovered stack: its name and the path to its plan JSON.
type Stack struct {
	Name string // dir containing the plan, relative to the scan root (forward-slash)
	Plan string // filesystem path to the tfplan.json
}

// Scan walks dir and returns one Stack per tfplan.json found, sorted
// lexicographically by Name. A nonexistent dir is an error; an existing dir
// with no plan files returns an empty slice and no error.
func Scan(dir string) ([]Stack, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plans-dir %q is not a directory", dir)
	}

	var stacks []Stack
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != PlanFile {
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(path))
		if err != nil {
			return err
		}
		stacks = append(stacks, Stack{Name: filepath.ToSlash(rel), Plan: path})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/plandir/`
Expected: PASS (4 tests, ok).

- [ ] **Step 5: Commit**

```bash
git add internal/plandir/
git commit -m "plandir: scan a directory tree for per-stack tfplan.json"
```

---

### Task 2: Migrate `cmd/tfstackplan` to `--plans-dir`

This task changes `main.go` and **both** cmd test files together, because they are one compilation unit: removing `opts.stacks`/`opts.manifestPath` breaks any test still referencing them. Goldens are regenerated at the end.

**Files:**
- Modify: `cmd/tfstackplan/main.go` (flags ~55-74; `opts` struct 38-50; `run` input branch 95-120; manifest title/marker override 106-111; per-stack source dir 161-164)
- Rewrite: `cmd/tfstackplan/main_test.go`
- Modify: `cmd/tfstackplan/examples_test.go` (helpers 38-134, 139-169; call sites 264-271, 312-319)

- [ ] **Step 1: Rewrite `main.go` input handling**

In `cmd/tfstackplan/main.go`:

(a) Replace the `manifest` import line (18) with the plandir import:

```go
	"github.com/Fluent-Health/terraform-stack-plan/internal/plandir"
```

(b) In the `opts` struct (38-50), remove `manifestPath` and `stacks`, add `plansDir`:

```go
type opts struct {
	plansDir  string
	title     string
	marker    string
	config    string
	maxBytes  int
	output    string
	classJSON string
	details   string
	repoRoot  string
	linkVars  []string
}
```

(c) In `main` (52-74), remove the `--manifest` and `--stack` flag registrations and the `var sf stackFlags` / `o.stacks = sf` lines; add `--plans-dir`. The resulting flag block:

```go
func main() {
	var o opts
	flag.StringVar(&o.plansDir, "plans-dir", "", "directory of per-stack plans (each <stack>/tfplan.json)")
	flag.StringVar(&o.title, "title", "Terraform plan", "report title")
	flag.StringVar(&o.marker, "marker", "tfstackplan", "HTML-comment marker for CI upsert")
	flag.StringVar(&o.config, "config", "", "HCL policy file (default: auto-discover .tfstackplan.hcl)")
	flag.IntVar(&o.maxBytes, "max-bytes", defaultMaxBytes, "document byte budget (0 disables)")
	flag.StringVar(&o.output, "output", "-", "output file ('-' = stdout)")
	flag.StringVar(&o.classJSON, "emit-classification-json", "", "write computed classes as JSON")
	flag.StringVar(&o.details, "details", "closed", "details disclosure: auto|open|closed")
	flag.StringVar(&o.repoRoot, "repo-root", ".", "repo root for computing link file paths")
	var lv stackFlags
	flag.Var(&lv, "link-var", "link template variable as key=value (repeatable); sha=<sha> also derives sha_short")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("tfstackplan", version)
		return
	}
	o.linkVars = lv

	out, fits, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan:", err)
		os.Exit(1)
	}
	if o.output == "-" || o.output == "" {
		fmt.Print(out)
	} else if err := os.WriteFile(o.output, []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "tfstackplan:", err)
		os.Exit(1)
	}
	if !fits {
		fmt.Fprintln(os.Stderr, "tfstackplan: warning: report exceeds --max-bytes even after full reduction")
		os.Exit(2)
	}
}
```

(`stackFlags` is still defined and now used only by `--link-var`; keep the type.)

(d) Replace the input `switch` in `run` (95-120) with a single `--plans-dir` path. The block from the start of `run` through the stack-gathering loop now reads:

```go
func run(o opts) (string, bool, error) {
	if o.plansDir == "" {
		return "", false, fmt.Errorf("no input: pass --plans-dir")
	}
	refs, err := plandir.Scan(o.plansDir)
	if err != nil {
		return "", false, err
	}

	var cfg *config.Config
	cfgPath := o.config
	if cfgPath == "" {
		if p, ok := config.Discover("."); ok {
			cfgPath = p
		}
	}
	if cfgPath != "" {
		c, err := config.Load(cfgPath)
		if err != nil {
			return "", false, err
		}
		cfg = c
	} else {
		cfg = &config.Config{Diff: config.DiffConfig{Detect: true}}
	}
	classified := cfg.Classification != nil

	report := model.Report{Title: o.title, Marker: o.marker, Classified: classified}
	base := baseVars(o.linkVars)
	if cfg.Links != nil {
		for _, l := range cfg.Links.Header {
			if url := links.Resolve(l.URL, base); url != "" {
				report.HeaderLinks = append(report.HeaderLinks, model.Link{Label: links.Resolve(l.Label, base), URL: url})
			}
		}
	}
	sidecar := map[string]classEntry{}
	for _, ref := range refs {
		data, err := os.ReadFile(ref.Plan)
		if err != nil {
			return "", false, fmt.Errorf("stack %q: %w", ref.Name, err)
		}
		raw, err := plan.Parse(ref.Name, data)
		if err != nil {
			return "", false, err
		}
		st := model.Stack{Name: ref.Name, Counts: raw.Counts}

		stackDir := filepath.Join(o.repoRoot, filepath.FromSlash(ref.Name))
		stackVars := mergeVars(base, map[string]string{"stack": ref.Name, "stack_dir": relSlash(o.repoRoot, stackDir)})
		var srcIdx *source.Index
		if cfg.Links != nil {
			st.URL = links.Resolve(cfg.Links.Stack, stackVars)
			if cfg.Links.Resource != "" {
				srcIdx = source.Build(stackDir, o.repoRoot)
			}
		}
```

The remainder of the loop body (the `if classified { … }` block, the `for _, rc := range raw.Changes` block, and `report.Stacks = append(...)`) is **unchanged** — leave lines 174-222 exactly as they are. Only the three lines that previously computed `stackDir` from `ref.Dir`/`filepath.Dir(ref.Plan)` are replaced by the single `stackDir := filepath.Join(...)` above.

`ref` is now a `plandir.Stack` (fields `Name`, `Plan`), so `ref.Name` and `ref.Plan` keep working; `ref.Dir` no longer exists and its usage is gone.

- [ ] **Step 2: Rewrite `main_test.go`**

Replace the entire contents of `cmd/tfstackplan/main_test.go` with:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const planJSON = `{
  "format_version": "1.2",
  "resource_changes": [
    {"address":"google_project_iam_member.editor","type":"google_project_iam_member","name":"editor",
     "change":{"actions":["update"],"before":{"role":"roles/viewer"},"after":{"role":"roles/editor"},
       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
  ]
}`

const cfgHCL = `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon = "🔐"
  }
}
`

// writePlan creates <root>/<name>/tfplan.json (name may contain forward slashes).
func writePlan(t *testing.T, root, name, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name), "tfplan.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunPlansDir(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "platform/nonprod", planJSON)
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, _, err := run(opts{
		plansDir:  plansDir,
		title:     "Terraform plan — nonprod",
		marker:    "tfstackplan:nonprod",
		config:    cfgPath,
		maxBytes:  60000,
		classJSON: classOut,
		details:   "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "<!-- tfstackplan:nonprod -->") {
		t.Fatalf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "🔐 iam") {
		t.Fatalf("iam classification not applied:\n%s", out)
	}
	data, err := os.ReadFile(classOut)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]struct {
		Class string `json:"class"`
		Icon  string `json:"icon"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["platform/nonprod"].Class != "iam" {
		t.Fatalf("sidecar class = %q, want iam", got["platform/nonprod"].Class)
	}
}

func TestRunMissingPlansDirFlag(t *testing.T) {
	_, _, err := run(opts{maxBytes: 60000})
	if err == nil {
		t.Fatal("expected error when --plans-dir is absent")
	}
}

func TestRunNonexistentPlansDir(t *testing.T) {
	_, _, err := run(opts{plansDir: filepath.Join(t.TempDir(), "nope"), maxBytes: 60000})
	if err == nil {
		t.Fatal("expected error for nonexistent plans-dir")
	}
}

func TestRunNoConfigNoClassColumn(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "s", planJSON)
	out, _, err := run(opts{
		plansDir: plansDir,
		title:    "T",
		marker:   "m",
		maxBytes: 60000,
		details:  "closed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Class") {
		t.Fatalf("no config → no Class column:\n%s", out)
	}
}

func TestRunEmitsLinks(t *testing.T) {
	dir := t.TempDir()
	// Source tree lives at the repo root, under the stack's path.
	srcDir := filepath.Join(dir, "platform", "nonprod")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.tf"),
		[]byte("resource \"google_project_iam_member\" \"editor\" {\n  role = \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plans live in a separate out/ tree.
	plansDir := filepath.Join(dir, "out")
	writePlan(t, plansDir, "platform/nonprod", planJSON)
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(`links {
  resource = "https://gh/o/r/blob/{sha}/{file}#L{line}"
  stack    = "https://gh/o/r/tree/{sha}/{stack_dir}"
  header {
    label = "PR #{pr}"
    url   = "https://gh/o/r/pull/{pr}"
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, err := run(opts{
		plansDir: plansDir,
		config:   cfgPath,
		maxBytes: 60000,
		details:  "open",
		repoRoot: dir,
		linkVars: []string{"sha=abc1234", "pr=42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[PR #42](https://gh/o/r/pull/42)") {
		t.Fatalf("header link missing:\n%s", out)
	}
	// Source dir is join(repoRoot, name), so the file path is name-relative.
	if !strings.Contains(out, "https://gh/o/r/blob/abc1234/platform/nonprod/main.tf#L1") {
		t.Fatalf("resource link missing:\n%s", out)
	}
	if !strings.Contains(out, "tree/abc1234/platform/nonprod") {
		t.Fatalf("stack link missing:\n%s", out)
	}
}

func TestRunEmitsClassificationAttributes(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")

	iamPlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.a","type":"google_project_iam_member","name":"a",
	     "change":{"actions":["create"],"after":{"role":"roles/x","project":"fh-host-nonprod"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"google_project_iam_member.b","type":"google_project_iam_member","name":"b",
	     "change":{"actions":["create"],"after":{"role":"roles/y","project":"fh-svc-dev"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	safePlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_storage_bucket.b","type":"google_storage_bucket","name":"b",
	     "change":{"actions":["create"],"after":{"name":"bkt","project":"fh-data"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	cfg := `
classification {
  default {
    name = "safe"
    icon = "✅"
  }
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }
}
`
	writePlan(t, plansDir, "platform/nonprod", iamPlan)
	writePlan(t, plansDir, "data/warehouse", safePlan)
	cfgPath := filepath.Join(dir, "cfg.hcl")
	classOut := filepath.Join(dir, "classes.json")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := run(opts{
		plansDir:  plansDir,
		config:    cfgPath,
		maxBytes:  60000,
		classJSON: classOut,
		details:   "closed",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(classOut)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]struct {
		Class      string              `json:"class"`
		Attributes map[string][]string `json:"attributes"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	iam := got["platform/nonprod"]
	want := []string{"fh-host-nonprod", "fh-svc-dev"}
	if len(iam.Attributes["project"]) != 2 || iam.Attributes["project"][0] != want[0] || iam.Attributes["project"][1] != want[1] {
		t.Fatalf("iam attributes.project = %v, want %v", iam.Attributes["project"], want)
	}
	if safe := got["data/warehouse"]; safe.Attributes != nil {
		t.Fatalf("safe stack must not emit attributes, got %v", safe.Attributes)
	}
	if strings.Contains(string(data), "fh-data") {
		t.Fatal("safe stack must not emit attributes (omitempty); found its project in raw JSON")
	}
}

func TestRunEmptyPlansDir(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(plansDir, 0o755); err != nil { // exists, no plans
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, fits, err := run(opts{
		plansDir:  plansDir,
		config:    cfgPath,
		maxBytes:  60000,
		classJSON: classOut,
		details:   "closed",
		title:     "Terraform plan — nonprod",
		marker:    "tf-plan:nonprod",
	})
	if err != nil {
		t.Fatalf("empty plans-dir run should not error: %v", err)
	}
	if !fits {
		t.Fatal("empty report should fit the budget")
	}
	if !strings.HasPrefix(out, "<!-- tf-plan:nonprod -->") {
		t.Fatalf("missing marker:\n%s", out)
	}
	if !strings.Contains(out, "(0 stacks changed)") {
		t.Fatalf("missing 0-stacks heading:\n%s", out)
	}
	if strings.Contains(out, "| Stack |") {
		t.Fatalf("empty report must not render a table:\n%s", out)
	}
	data, err := os.ReadFile(classOut)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("sidecar should be empty object, got: %s", data)
	}
}
```

- [ ] **Step 3: Migrate `examples_test.go` helpers and call sites**

In `cmd/tfstackplan/examples_test.go`:

(a) Change `exampleStacks` to write the `out/` tree and return the plans dir. Replace its signature line (38) and its trailing write loop (124-134):

Signature + doc comment (35-38) becomes:

```go
// exampleStacks writes the shared multi-stack input (56 changes across 8 stacks,
// including IAM, sensitive, destructive, structural and large-diff resources)
// into an out/<name>/tfplan.json tree and returns the plans dir.
func exampleStacks(t *testing.T, dir string) string {
```

Trailing loop (124-134) becomes:

```go
	plansDir := filepath.Join(dir, "out")
	for _, s := range stacks {
		p := filepath.Join(plansDir, filepath.FromSlash(s.name), "tfplan.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, genPlan(s.changes...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return plansDir
}
```

(b) Apply the identical change to `stateOpsStacks` — signature (139) becomes `func stateOpsStacks(t *testing.T, dir string) string {` and its trailing loop (159-169) becomes the same `plansDir := ...; for ... { mkdir + write tfplan.json }; return plansDir` block as in (a).

(c) Update the two call sites. In `TestExamples` (264-271):

```go
			out, fits, err := run(opts{
				plansDir: stacks,
				title:    "Terraform plan — nonprod",
				marker:   "tfstackplan:nonprod",
				config:   cfgPath,
				maxBytes: sc.maxBytes,
				details:  "closed",
			})
```

In `TestStateOpsExample` (312-319):

```go
	out, fits, err := run(opts{
		plansDir: stacks,
		title:    "Terraform plan — state ops & structured diffs",
		marker:   "tfstackplan:state-ops",
		config:   cfgPath,
		maxBytes: 60000,
		details:  "closed",
	})
```

(The local variable is still named `stacks`; it now holds the plans-dir path string.)

- [ ] **Step 4: Run cmd tests — expect golden mismatch only**

Run: `go test ./cmd/tfstackplan/`
Expected: `TestRunPlansDir`, `TestRunMissingPlansDirFlag`, `TestRunNonexistentPlansDir`, `TestRunNoConfigNoClassColumn`, `TestRunEmitsLinks`, `TestRunEmitsClassificationAttributes`, `TestRunEmptyPlansDir` PASS. `TestExamples`/`TestStateOpsExample` FAIL with "is stale; run -update" because stacks now sort lexicographically (reordered output). If any test other than the golden ones fails, fix before continuing.

- [ ] **Step 5: Regenerate the goldens**

Run: `go test ./cmd/tfstackplan/ -update`
Then verify: `go test ./cmd/tfstackplan/`
Expected: all PASS.

- [ ] **Step 6: Inspect the regenerated goldens**

Run: `git diff --stat examples/` and skim `git diff examples/big-plan.md | head -60`.
Expected: stacks reordered alphabetically (e.g. `data/warehouse` now precedes `platform/nonprod`); content otherwise equivalent. Confirm no stack was dropped (still 8 in big-plan).

- [ ] **Step 7: Commit**

```bash
git add cmd/tfstackplan/ examples/
git commit -m "cmd: --plans-dir directory-scan input; regenerate example goldens"
```

---

### Task 3: Remove `internal/manifest` and the manifest example

**Files:**
- Delete: `internal/manifest/` (whole dir)
- Delete: `examples/manifest.yaml`

- [ ] **Step 1: Confirm nothing still imports manifest**

Run: `grep -rn "internal/manifest" --include="*.go" .`
Expected: no output (Task 2 removed the only import).

- [ ] **Step 2: Delete the package and example**

```bash
git rm -r internal/manifest
git rm examples/manifest.yaml
```

- [ ] **Step 3: Tidy modules**

Run: `go mod tidy`
Expected: no error. `gopkg.in/yaml.v3` remains in `go.mod` (still used by `internal/differ`); `git diff go.mod go.sum` should show no change or only incidental tidying.

- [ ] **Step 4: Full build and test**

Run: `go build ./... && go test ./...`
Expected: all packages build and PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "manifest: remove the manifest/--stack input layer"
```

---

### Task 4: Documentation

**Files:**
- Modify: `README.md` (Usage 161-192; Sidecar example 254; CLI reference 372-389)
- Modify: `docs/DESIGN.md` (the "pure renderer" line 22-24; Inputs section 106-151)

- [ ] **Step 1: Rewrite the README Usage section**

In `README.md`, replace the Usage section (the inline `--stack` example, the manifest YAML block, and the "Or describe the run in a manifest" / "The manifest carries only…" prose, lines ~161-195) with:

````markdown
## Usage

Each stack contributes one `tfplan.json` (`terraform show -json plan.bin`).
Collect them under one directory that mirrors your stack tree — `out/<stack>/tfplan.json` —
and point the tool at it:

```bash
tfstackplan --plans-dir out/ \
  --title  "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

Each `tfplan.json` found defines a stack; its **name** is the directory holding
it, relative to `--plans-dir` (so `out/platform/nonprod/tfplan.json` →
`platform/nonprod`). Stacks render in alphabetical order. An empty (or absent)
set of plans renders a header-only "0 stacks changed" report.

### Driving from your orchestrator

**Terramate** — a per-stack `script` writes each plan into the central tree
(`terramate.stack.path.to_root` climbs back to the repo root), then one render
step rolls them up:

```hcl
script "plan-report" {
  job {
    commands = [
      ["terraform", "plan", "-out", "tfplan.bin"],
      ["sh", "-c", "mkdir -p ${terramate.stack.path.to_root}/out/${terramate.stack.path.relative} && terraform show -json tfplan.bin > ${terramate.stack.path.to_root}/out/${terramate.stack.path.relative}/tfplan.json"],
    ]
  }
}
```

```bash
terramate script run plan-report
tfstackplan --plans-dir out/ --output report.md
```

**Terragrunt** — its native `--json-out-dir` already produces the right shape:

```bash
terragrunt run --all --filter-affected plan --json-out-dir out
tfstackplan --plans-dir out --output report.md
```

The source-aware links feature resolves each resource against
`<repo-root>/<stack name>`, so it works automatically when `out/` mirrors the
stack tree (the default above). Set `--repo-root` if you run the tool from
elsewhere.
````

- [ ] **Step 2: Fix the remaining README command examples**

In `README.md`, update the two later example commands that still use `--manifest`:

- The Sidecar example (~254-255): replace `--manifest plan.yaml` with `--plans-dir out/`.
- The Links example (~349-352): it already uses `--manifest plan.yaml` — replace with `--plans-dir out/`.

Run to find any stragglers: `grep -n -- "--manifest\|--stack " README.md`
Expected after edits: no matches outside the `kubernetes_manifest` diff examples (those are unrelated resource names, leave them).

- [ ] **Step 3: Rewrite the README CLI reference block**

Replace the CLI reference code block and the paragraph after it (~374-389) with:

````markdown
```
tfstackplan --plans-dir DIR
            [--title TEXT] [--marker TEXT]
            [--config FILE]                 # HCL policy; default: auto-discover .tfstackplan.hcl
            [--max-bytes N]                 # default 60000; 0 disables
            [--details auto|open|closed]    # default closed (auto = open iff one stack changed)
            [--emit-classification-json FILE]
            [--repo-root DIR]               # base for link file paths (default ".")
            [--link-var key=value]          # link template var (repeatable)
            [--output FILE | -]             # default '-' (stdout)
            [--version]
```

`--plans-dir` is required; it is scanned for `tfplan.json` files. With no
`--config` and no `.tfstackplan.hcl` present, classification is off, diffs use
defaults, and no links are emitted.
````

- [ ] **Step 4: Update DESIGN.md**

In `docs/DESIGN.md`:

(a) The "pure renderer" paragraph (~22-24): change "It reads `plan.json` files" to "It scans a directory of per-stack `tfplan.json` files".

(b) The Inputs section (~106-151): replace the "Manifest (per-run, YAML or JSON)" and "Or via flags" subsections with a single subsection describing `--plans-dir` (directory scan, name = dir relative to root, fixed `tfplan.json` filename, alphabetical order, source dir = `join(repo-root, name)`). Update the "CLI surface" listing to match Step 3's flag set. Add a one-line pointer: "Background and the orchestrator-integration rationale: `docs/terramate-integration.md`."

- [ ] **Step 5: Verify docs have no dead references**

Run: `grep -rn -- "--manifest\|--stack \|manifest.yaml\|ParseStackFlags" README.md docs/DESIGN.md`
Expected: no matches (kubernetes_manifest diff examples excepted).

- [ ] **Step 6: Commit**

```bash
git add README.md docs/DESIGN.md docs/terramate-integration.md docs/superpowers/
git commit -m "docs: --plans-dir usage, orchestrator recipes, integration findings + spec/plan"
```

---

## Self-Review

**Spec coverage** (against `2026-05-30-plans-dir-input-design.md`):
- Single input mode `--plans-dir`; remove `--manifest`/`--stack`/`manifest` pkg → Tasks 2, 3. ✓
- Canonical `tfplan.json`, no flag → `plandir.PlanFile` (Task 1). ✓
- Recursive scan; name = dir relative to root → `Scan` + `TestScanNamesAndSorts` (Task 1). ✓
- Source dir = `join(--repo-root, name)` → Task 2 Step 1(d) + `TestRunEmitsLinks`. ✓
- Lexicographic order → `Scan` sort + `TestScanNamesAndSorts`; example reorder (Task 2). ✓
- Title/marker from flags → Task 2 (override block removed). ✓
- Absent flag → error; nonexistent dir → error; empty dir → 0-stack report → `TestRunMissingPlansDirFlag`, `TestRunNonexistentPlansDir`, `TestRunEmptyPlansDir`. ✓
- Classify/diff/links/sidecar/fit/render unchanged → loop body left intact (Task 2 Step 1(d)). ✓
- Orchestrator recipes (Terramate + Terragrunt) → Task 4 Step 1. ✓
- Downstream consumer migration note → spec Risks (no code here); flagged in handoff below. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code; commands have expected output. ✓

**Type consistency:** `plandir.Stack{Name, Plan}` defined in Task 1, used in Task 2; `Scan(dir) ([]Stack, error)` signature consistent; `opts.plansDir` field name consistent across `main.go` and all tests; `PlanFile = "tfplan.json"` is the single source of the filename. ✓

---

## Out of scope (per spec)

- `--stacks-from` / stdin input (superseded).
- Any orchestrator-specific code in tfstackplan.
- The **infra plan-trigger migration** (write empty manifest → `mkdir -p out && tfstackplan --plans-dir out`) lives in the `infra` repo, not here. Ship this as a minor version bump and update the `TFSP_VER` pin there in a coordinated follow-up.
