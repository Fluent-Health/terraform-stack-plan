# Empty (0-stacks) report — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make an explicit empty manifest (`stacks: []`) valid input that renders `<!-- marker -->` + `### <title>  (0 stacks changed)` (no table/details), emits `{}` for `--emit-classification-json`, and exits 0 — while neither-flag invocation still errors.

**Architecture:** Drop the empty-stacks guard in `manifest.Load`; short-circuit `render.Render` to header-only when there are zero stacks; `cmd` needs no change (the zero-length gather loop and existing sidecar write already produce `{}`).

**Tech Stack:** Go, `gopkg.in/yaml.v3` (manifest), standard `testing`.

**Spec:** `docs/superpowers/specs/2026-05-29-empty-report-design.md`

---

## File Structure

| File | Responsibility | Action |
|------|----------------|--------|
| `internal/manifest/manifest.go` | Remove the `len(Stacks)==0 → error` guard in `Load`. | Modify |
| `internal/manifest/manifest_test.go` | Test: empty `stacks: []` loads, zero refs. | Modify |
| `internal/render/render.go` | Short-circuit `Render` to header-only when `len(r.Stacks)==0`. | Modify |
| `internal/render/render_test.go` | Test: zero-stack report = marker + `(0 stacks changed)`, no table; header links render. | Modify |
| `cmd/tfstackplan/main_test.go` | E2E: empty manifest → no error, `(0 stacks changed)`, `{}` sidecar. | Modify |

---

## Task 1: Allow an empty manifest in `manifest.Load`

**Files:**
- Modify: `internal/manifest/manifest.go` (`Load`, the `len(m.Stacks) == 0` guard ~line 38-40)
- Test: `internal/manifest/manifest_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/manifest/manifest_test.go`:

```go
func TestLoadEmptyStacksIsValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(p, []byte("title: \"T\"\nmarker: \"m\"\nstacks: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(p)
	if err != nil {
		t.Fatalf("empty manifest should load, got error: %v", err)
	}
	if len(m.Stacks) != 0 {
		t.Fatalf("want 0 stacks, got %d", len(m.Stacks))
	}
	if m.Title != "T" || m.Marker != "m" {
		t.Fatalf("title/marker not parsed: %+v", m)
	}
}
```

Ensure the test file imports `os` and `path/filepath` (add to the import block if absent — check the top of `manifest_test.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manifest/ -run TestLoadEmptyStacksIsValid -v`
Expected: FAIL — `Load` returns `manifest <path>: no stacks`.

- [ ] **Step 3: Remove the guard.** In `internal/manifest/manifest.go`, delete these lines from `Load`:

```go
	if len(m.Stacks) == 0 {
		return Manifest{}, fmt.Errorf("manifest %s: no stacks", path)
	}
```

So `Load` ends with `return m, nil` immediately after the `yaml.Unmarshal` error check. (Leave `ParseStackFlags` unchanged — `--stack` flags still require a non-empty value each; passing zero `--stack` flags falls through to `cmd`'s `default` no-input error.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manifest/ -v`
Expected: PASS (new test + existing `TestLoadYAML`, `TestParseStackFlags`, `TestParseStackFlagInvalid`).

- [ ] **Step 5: Commit**

```bash
git add internal/manifest/manifest.go internal/manifest/manifest_test.go
git commit -m "manifest: allow an explicit empty stacks list"
```

---

## Task 2: Short-circuit `render.Render` for zero stacks

**Files:**
- Modify: `internal/render/render.go` (`Render`, top of function ~line 13-17)
- Test: `internal/render/render_test.go`

- [ ] **Step 1: Write the failing test** — append to `internal/render/render_test.go`:

```go
func TestRenderEmptyReport(t *testing.T) {
	r := model.Report{
		Title:       "Terraform plan — nonprod",
		Marker:      "tf-plan:nonprod",
		Classified:  true,
		Stacks:      nil,
		HeaderLinks: []model.Link{{Label: "PR #42", URL: "https://gh/o/r/pull/42"}},
	}
	out := Render(r)
	if !strings.HasPrefix(out, "<!-- tf-plan:nonprod -->\n") {
		t.Fatalf("missing marker line:\n%s", out)
	}
	if !strings.Contains(out, "### Terraform plan — nonprod  (0 stacks changed)") {
		t.Fatalf("missing 0-stacks heading:\n%s", out)
	}
	if strings.Contains(out, "| Stack |") {
		t.Fatalf("empty report must not render a table:\n%s", out)
	}
	if !strings.Contains(out, "[PR #42](https://gh/o/r/pull/42)") {
		t.Fatalf("header links should still render:\n%s", out)
	}
}
```

NOTE: `render_test.go` is `package render` (internal test) — call `Render(r)`, not `render.Render(r)`. Match the existing tests in the file (they call `Render(...)` directly).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestRenderEmptyReport -v`
Expected: FAIL — current `Render` calls `renderTable` for zero stacks, emitting a header-only table (so `| Stack |` is present), failing the no-table assertion.

- [ ] **Step 3: Add the short-circuit.** In `internal/render/render.go`, in `Render`, immediately after the marker line and before the `switch r.Mode`:

```go
func Render(r model.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- %s -->\n", r.Marker)

	// Nothing changed: heading ("(0 stacks changed)") + header links only —
	// no summary table, no details. Reachable via an explicit empty manifest.
	if len(r.Stacks) == 0 {
		renderHeader(&b, r)
		return b.String()
	}

	switch r.Mode {
	// …unchanged…
```

(`renderHeader` already prints `### <title>  (0 stacks changed)` via `changedStacks`, which returns 0 for an empty report, then the header-links block.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/render/ -v`
Expected: PASS (new test + all existing render tests, which use non-empty reports and are unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/render/render.go internal/render/render_test.go
git commit -m "render: header-only output for a zero-stack report"
```

---

## Task 3: End-to-end — empty manifest run

**Files:**
- Test: `cmd/tfstackplan/main_test.go` (no production change in `main.go`)

- [ ] **Step 1: Write the failing test** — append to `cmd/tfstackplan/main_test.go`:

```go
func TestRunEmptyManifest(t *testing.T) {
	dir := t.TempDir()
	manPath := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(manPath,
		[]byte("title: \"Terraform plan — nonprod\"\nmarker: \"tf-plan:nonprod\"\nstacks: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "cfg.hcl")
	if err := os.WriteFile(cfgPath, []byte(cfgHCL), 0o644); err != nil {
		t.Fatal(err)
	}
	classOut := filepath.Join(dir, "classes.json")

	out, fits, err := run(opts{
		manifestPath: manPath,
		config:       cfgPath,
		maxBytes:     60000,
		classJSON:    classOut,
		details:      "closed",
	})
	if err != nil {
		t.Fatalf("empty manifest run should not error: %v", err)
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

(Reuses the existing package-level `cfgHCL` const in `main_test.go`, which defines a classification block with the `iam` preset.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/tfstackplan/ -run TestRunEmptyManifest -v`
Expected: FAIL at this point only if Tasks 1–2 are not yet applied (manifest still errors / table still rendered). After Tasks 1–2 it should pass — run it to confirm the end-to-end wiring.

- [ ] **Step 3: Confirm no production code change is needed.** Read the `run` input switch in `cmd/tfstackplan/main.go`: the `--manifest` branch sets `refs = m.Stacks` (now possibly empty) and the per-stack loop simply iterates zero times; the `o.classJSON` write marshals the empty `sidecar` map to `{}`. If the test passes with no `main.go` edit, that's expected. If it fails, STOP and report what broke — do not add speculative code.

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
git add cmd/tfstackplan/main_test.go
git commit -m "cmd: e2e test for empty-manifest 0-stacks render + {} sidecar"
```

---

## Final verification

- [ ] **Full suite + vet + fmt**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all PASS; `gofmt -l` prints nothing.

- [ ] **Manual smoke test mirroring the CI consumer**

```bash
go build -o /tmp/tfstackplan ./cmd/tfstackplan
cat > /tmp/empty.yaml <<'YAML'
title: "Terraform plan — nonprod"
marker: "tf-plan:nonprod"
stacks: []
YAML
cat > /tmp/policy.hcl <<'HCL'
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
HCL
/tmp/tfstackplan --manifest /tmp/empty.yaml --config /tmp/policy.hcl \
  --emit-classification-json /tmp/classes.json --output -
echo "exit=$?"
echo "--- sidecar ---"; cat /tmp/classes.json
```
Expected: stdout is the marker line + `### Terraform plan — nonprod  (0 stacks changed)`, no table; `exit=0`; sidecar prints `{}`.

---

## Self-review notes

- **Spec coverage:** empty manifest valid (T1) ✓; neither-flag still errors (unchanged `cmd` default — verified in T3 Step 3) ✓; header-only `(0 stacks changed)`, no table/details (T2) ✓; header links still render (T2 test) ✓; `{}` sidecar (T3) ✓.
- **No production change in `cmd`** is intentional and explicitly verified (T3 Step 3), not assumed silently.
- **Placeholder scan:** all steps carry concrete code/commands.
- **Consistency:** `Render` short-circuits on `len(r.Stacks) == 0`; `manifest.Load` returns `m, nil` for empty; the e2e asserts the same `(0 stacks changed)` string and `{}` sidecar that the render/manifest tasks produce.
- **Post-merge:** cut **v0.4.1**, then bump `TFSP_VER` in the infra trigger + add the zero-change manifest call and `gh_upsert_comment` marker-idempotency fix (separate infra plan).
