# Source-aware links — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit clickable links — report header (build/PR/commit), stack heading (→ dir at commit), and resource address (→ its `.tf` declaration `file#Lline` at the commit, resolved by parsing the source) — driven by HCL templates + run flags.

**Architecture:** A new `internal/source` package parses each stack's `.tf` (and modules resolved via `.terraform/modules/modules.json`) into a `(module, type, name) → file:line` index. A new `internal/links` package does pure `{var}` templating. `config` parses a `links {}` block; `cmd` builds the index, resolves URLs from templates + tool/run vars, and stores plain URL strings on `model`; `render` wraps text in `<a>`. Unresolvable resources fall back to the stack link.

**Tech Stack:** Go 1.23; `hashicorp/hcl/v2/hclsyntax` (already a dep) for parsing `.tf`; stdlib `encoding/json`, `path/filepath`.

---

## File Structure

- `internal/links/links.go` (**new**) — `Resolve(template string, vars map[string]string) string`.
- `internal/source/source.go` (**new**) — `Index`, `Build(dir, repoRoot string) *Index`, `(*Index).Lookup(moduleAddress, typ, name) (Loc, bool)`.
- `internal/config/config.go` — parse `links {}` → `Config.Links *LinksConfig`.
- `internal/manifest/manifest.go` — `StackRef.Dir`.
- `internal/plan/plan.go` — `RawChange.Name`, `RawChange.ModuleAddress`.
- `internal/model/model.go` — `Link`, `Report.HeaderLinks`, `Stack.URL`, `Change.URL`.
- `internal/render/render.go` — header links line; `<a>` around stack name and resource address.
- `cmd/tfstackplan/main.go` — `--repo-root`, `--link-var` flags; build index; resolve + fill model.

Define `links.Resolve` (Task 1), `source` types (Task 3), `model` link fields (Task 5) before the wiring (Task 7) references them.

---

## Task 1: internal/links — pure templating

**Files:**
- Create: `internal/links/links.go`
- Test: `internal/links/links_test.go`

- [ ] **Step 1: Write the failing test**

```go
package links

import "testing"

func TestResolve(t *testing.T) {
	vars := map[string]string{"sha": "abc1234567", "file": "main.tf", "line": "12"}
	cases := []struct{ name, tmpl, want string }{
		{"all present", "b/{sha}/{file}#L{line}", "b/abc1234567/main.tf#L12"},
		{"literal only", "https://x/y", "https://x/y"},
		{"missing var omits", "b/{sha}/{nope}", ""},
		{"empty var omits", "b/{empty}", ""},
		{"repeated var", "{sha}-{sha}", "abc1234567-abc1234567"},
	}
	vars["empty"] = ""
	for _, c := range cases {
		if got := Resolve(c.tmpl, vars); got != c.want {
			t.Errorf("%s: Resolve(%q) = %q, want %q", c.name, c.tmpl, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/links/ -run TestResolve -v`
Expected: FAIL — package/function undefined.

- [ ] **Step 3: Implement**

```go
// Package links resolves URL templates with {placeholder} substitution.
package links

import "strings"

// Resolve substitutes {key} placeholders in tmpl from vars. If tmpl references
// any key that is missing or empty, Resolve returns "" — callers treat that as
// "no link", so partially-configured runs degrade cleanly. tmpl with no
// placeholders is returned verbatim.
func Resolve(tmpl string, vars map[string]string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(tmpl, '{')
		if open < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		close := strings.IndexByte(tmpl[open:], '}')
		if close < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		close += open
		b.WriteString(tmpl[:open])
		key := tmpl[open+1 : close]
		val, ok := vars[key]
		if !ok || val == "" {
			return ""
		}
		b.WriteString(val)
		tmpl = tmpl[close+1:]
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/links/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/links/
git commit -m "links: pure {var} template resolution"
```

---

## Task 2: internal/config — parse the links {} block

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`, `internal/config/testdata/links.hcl` (new)

`Config` currently is `{ Classification *Classification; Diff DiffConfig }`. Add `Links *LinksConfig` and a `case "links"` to the block switch in `Load`.

- [ ] **Step 1: Write the failing test + fixture**

Create `internal/config/testdata/links.hcl`:

```hcl
links {
  resource = "https://gh/o/r/blob/{sha}/{file}#L{line}"
  stack    = "https://gh/o/r/tree/{sha}/{stack_dir}"
  header {
    label = "Build #{build_id}"
    url   = "https://cb/{build_id}"
  }
  header {
    label = "PR #{pr}"
    url   = "https://gh/o/r/pull/{pr}"
  }
}
```

Append to `internal/config/config_test.go`:

```go
func TestLoadLinks(t *testing.T) {
	cfg, err := Load("testdata/links.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Links == nil {
		t.Fatal("expected Links to be parsed")
	}
	if cfg.Links.Resource != "https://gh/o/r/blob/{sha}/{file}#L{line}" {
		t.Errorf("resource template = %q", cfg.Links.Resource)
	}
	if cfg.Links.Stack != "https://gh/o/r/tree/{sha}/{stack_dir}" {
		t.Errorf("stack template = %q", cfg.Links.Stack)
	}
	if len(cfg.Links.Header) != 2 || cfg.Links.Header[0].Label != "Build #{build_id}" || cfg.Links.Header[1].URL != "https://gh/o/r/pull/{pr}" {
		t.Errorf("header = %+v", cfg.Links.Header)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadLinks -v`
Expected: FAIL — `cfg.Links` undefined.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add to the `Config` struct:

```go
	Links *LinksConfig // nil when no links block present
```

Add the types and decoder (near the diff decoder):

```go
// LinksConfig holds URL templates for header / stack / resource links.
type LinksConfig struct {
	Resource string       // template for a resource address; vars incl. {file},{line}
	Stack    string       // template for a stack heading; vars incl. {stack_dir}
	Header   []HeaderLink // report-header links
}

// HeaderLink is one templated report-header link.
type HeaderLink struct {
	Label string
	URL   string
}

type linksAttrs struct {
	Resource string `hcl:"resource,optional"`
	Stack    string `hcl:"stack,optional"`
}

type headerBlock struct {
	Label string `hcl:"label"`
	URL   string `hcl:"url"`
}

func decodeLinks(blk *hclsyntax.Block) (*LinksConfig, error) {
	lc := &LinksConfig{}
	body := blk.Body
	for name, target := range map[string]*string{"resource": &lc.Resource, "stack": &lc.Stack} {
		if a, ok := body.Attributes[name]; ok {
			var s string
			if d := gohcl.DecodeExpression(a.Expr, nil, &s); d.HasErrors() {
				return nil, fmt.Errorf("links.%s: %s", name, d.Error())
			}
			*target = s
		}
	}
	for _, b := range body.Blocks {
		if b.Type != "header" {
			return nil, fmt.Errorf("links: unknown block %q", b.Type)
		}
		var hb headerBlock
		if d := gohcl.DecodeBody(b.Body, nil, &hb); d.HasErrors() {
			return nil, fmt.Errorf("links.header: %s", d.Error())
		}
		lc.Header = append(lc.Header, HeaderLink{Label: hb.Label, URL: hb.URL})
	}
	return lc, nil
}
```

Add the case to the block switch in `Load`:

```go
		case "links":
			lc, err := decodeLinks(blk)
			if err != nil {
				return nil, err
			}
			cfg.Links = lc
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS (TestLoadLinks + existing). The existing `unknownblock.hcl` test still errors as before (links is now known, but that fixture uses a different unknown name).

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "config: parse links{} block (resource/stack templates + header blocks)"
```

---

## Task 3: internal/source — the .tf indexer

**Files:**
- Create: `internal/source/source.go`
- Test: `internal/source/source_test.go`, fixtures under `internal/source/testdata/`

- [ ] **Step 1: Write the failing test + fixtures**

Create fixtures:

`internal/source/testdata/stack/main.tf`:
```hcl
resource "google_storage_bucket" "state" {
  name = "x"
}

resource "google_project_iam_member" "editor" {
  role = "roles/editor"
}
```

`internal/source/testdata/stack/modules/net/net.tf`:
```hcl
resource "google_compute_firewall" "web" {
  name = "web"
}
```

`internal/source/testdata/stack/.terraform/modules/modules.json`:
```json
{"Modules":[
  {"Key":"","Source":"","Dir":"."},
  {"Key":"net","Source":"./modules/net","Dir":"modules/net"},
  {"Key":"remote","Source":"app.terraform.io/x/y","Dir":".terraform/modules/remote"}
]}
```

Create `internal/source/source_test.go`:

```go
package source

import "testing"

func TestIndexRootAndLocalModule(t *testing.T) {
	idx := Build("testdata/stack", "testdata")

	// root module resource → file:line relative to repoRoot ("testdata")
	loc, ok := idx.Lookup("", "google_storage_bucket", "state")
	if !ok {
		t.Fatal("root resource not found")
	}
	if loc.File != "stack/main.tf" || loc.Line != 1 {
		t.Errorf("root loc = %+v, want stack/main.tf:1", loc)
	}

	// instance key is stripped: caller passes the bare type/name
	if loc, ok := idx.Lookup("", "google_project_iam_member", "editor"); !ok || loc.Line != 5 {
		t.Errorf("second root resource loc = %+v ok=%v", loc, ok)
	}

	// local module resource via modules.json
	if loc, ok := idx.Lookup("module.net", "google_compute_firewall", "web"); !ok || loc.File != "stack/modules/net/net.tf" {
		t.Errorf("module resource loc = %+v ok=%v", loc, ok)
	}

	// unknown resource → miss
	if _, ok := idx.Lookup("", "google_storage_bucket", "missing"); ok {
		t.Error("expected miss for unknown resource")
	}

	// remote module (cached under .terraform) is not indexed → miss → caller falls back
	if _, ok := idx.Lookup("module.remote", "anything", "x"); ok {
		t.Error("remote module resources must not be indexed")
	}
}

func TestModuleKey(t *testing.T) {
	cases := map[string]string{"": "", "module.a": "a", "module.a.module.b": "a.b"}
	for in, want := range cases {
		if got := moduleKey(in); got != want {
			t.Errorf("moduleKey(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/ -v`
Expected: FAIL — package undefined.

- [ ] **Step 3: Implement**

```go
// Package source indexes a stack's Terraform source so a plan resource can be
// linked to the file:line where it is declared.
package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Loc is a resource declaration location, with File relative to the repo root.
type Loc struct {
	File string
	Line int
}

// Index maps (module, type, name) → declaration location.
type Index struct {
	m map[string]Loc
}

func key(moduleKey, typ, name string) string {
	return moduleKey + "\x00" + typ + "\x00" + name
}

// Build parses dir's *.tf (root module) plus any local modules listed in
// dir/.terraform/modules/modules.json, recording each resource block's
// location relative to repoRoot. Files outside repoRoot, unparseable files,
// and modules cached under .terraform are skipped. Build never fails: a missing
// or unreadable tree just yields a sparse index (callers fall back).
func Build(dir, repoRoot string) *Index {
	idx := &Index{m: map[string]Loc{}}
	idx.parseDir(dir, "", repoRoot)

	mjPath := filepath.Join(dir, ".terraform", "modules", "modules.json")
	if data, err := os.ReadFile(mjPath); err == nil {
		var doc struct {
			Modules []struct{ Key, Source, Dir string }
		}
		if json.Unmarshal(data, &doc) == nil {
			for _, m := range doc.Modules {
				if m.Key == "" || underDotTerraform(m.Dir) {
					continue // root handled above; cached remote modules skipped
				}
				idx.parseDir(filepath.Join(dir, m.Dir), m.Key, repoRoot)
			}
		}
	}
	return idx
}

func (idx *Index) parseDir(dir, moduleKey, repoRoot string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		f, diags := hclsyntax.ParseConfig(src, full, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			continue // skip a file we can't parse; never fail the run
		}
		rel := relTo(repoRoot, full)
		if rel == "" {
			continue // outside the repo → can't link
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, blk := range body.Blocks {
			if blk.Type != "resource" || len(blk.Labels) != 2 {
				continue
			}
			k := key(moduleKey, blk.Labels[0], blk.Labels[1])
			if _, exists := idx.m[k]; exists {
				continue // first declaration wins (override files)
			}
			idx.m[k] = Loc{File: rel, Line: blk.DefRange().Start.Line}
		}
	}
}

// Lookup resolves a plan resource. moduleAddress is the plan's module_address
// ("" for root, "module.a.module.b" for nested); the instance key is irrelevant.
func (idx *Index) Lookup(moduleAddress, typ, name string) (Loc, bool) {
	loc, ok := idx.m[key(moduleKey(moduleAddress), typ, name)]
	return loc, ok
}

// moduleKey turns a plan module_address ("module.a.module.b") into the
// modules.json key ("a.b"); root ("") stays "".
func moduleKey(moduleAddress string) string {
	if moduleAddress == "" {
		return ""
	}
	parts := strings.Split(moduleAddress, ".")
	var keys []string
	for i := 0; i < len(parts); i++ {
		if parts[i] == "module" && i+1 < len(parts) {
			keys = append(keys, parts[i+1])
			i++
		}
	}
	return strings.Join(keys, ".")
}

func underDotTerraform(dir string) bool {
	return dir == ".terraform" || strings.HasPrefix(dir, ".terraform/") || strings.Contains(dir, "/.terraform/")
}

// relTo returns target relative to root in slash form, or "" if target escapes
// root.
func relTo(root, target string) string {
	ra, err1 := filepath.Abs(root)
	ta, err2 := filepath.Abs(target)
	if err1 != nil || err2 != nil {
		return ""
	}
	rel, err := filepath.Rel(ra, ta)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/ -v`
Expected: PASS. (Note: the `.terraform` fixture dir must be committed; if a repo-level `.gitignore` ignores `.terraform`, `git add -f` the fixture in Step 5.)

- [ ] **Step 5: Commit**

```bash
git add -f internal/source/
git commit -m "source: index .tf resources (root + local modules via modules.json) to file:line"
```

---

## Task 4: input plumbing — RawChange + StackRef fields

**Files:**
- Modify: `internal/plan/plan.go`, `internal/plan/plan_test.go`
- Modify: `internal/manifest/manifest.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/plan/plan_test.go`:

```go
func TestParseCarriesModuleAndName(t *testing.T) {
	data := []byte(`{"format_version":"1.2","resource_changes":[
	  {"address":"module.net.google_compute_firewall.web","module_address":"module.net",
	   "type":"google_compute_firewall","name":"web",
	   "change":{"actions":["update"],"before":{"a":"1"},"after":{"a":"2"},
	     "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}]}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	c := rs.Changes[0]
	if c.Name != "web" || c.ModuleAddress != "module.net" {
		t.Fatalf("got name=%q module=%q", c.Name, c.ModuleAddress)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -run TestParseCarriesModuleAndName -v`
Expected: FAIL — `c.Name` / `c.ModuleAddress` undefined.

- [ ] **Step 3: Implement**

In `internal/plan/plan.go`, add to the `RawChange` struct:

```go
	Name          string
	ModuleAddress string
```

In `Parse`, set them on the `ch := RawChange{...}` literal:

```go
			Name:          rc.Name,
			ModuleAddress: rc.ModuleAddress,
```

In `internal/manifest/manifest.go`, add to `StackRef`:

```go
	Dir string `yaml:"dir" json:"dir"` // source dir; defaults to the plan file's directory
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plan/ ./internal/manifest/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plan/ internal/manifest/
git commit -m "plan/manifest: carry module_address + name; optional stack dir"
```

---

## Task 5: model — link fields

**Files:**
- Modify: `internal/model/model.go`
- Test: `internal/model/model_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestLinkFields(t *testing.T) {
	r := Report{HeaderLinks: []Link{{Label: "PR #1", URL: "https://x/1"}}}
	r.Stacks = []Stack{{Name: "s", URL: "https://x/s"}}
	r.Stacks[0].Changes = []Change{{Address: "a.b", URL: "https://x/a"}}
	if r.HeaderLinks[0].URL != "https://x/1" || r.Stacks[0].URL == "" || r.Stacks[0].Changes[0].URL == "" {
		t.Fatal("link fields not wired")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/model/ -run TestLinkFields -v`
Expected: FAIL — `Link` / `URL` / `HeaderLinks` undefined.

- [ ] **Step 3: Implement**

In `internal/model/model.go`:

```go
// Link is a labelled report-header link.
type Link struct {
	Label string
	URL   string
}
```

Add `URL string` to `Stack` and to `Change`; add `HeaderLinks []Link` to `Report`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/model/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/model/
git commit -m "model: header/stack/resource link fields"
```

---

## Task 6: render — emit the links

**Files:**
- Modify: `internal/render/render.go`
- Test: `internal/render/render_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRenderHeaderLinks(t *testing.T) {
	r := sampleReport()
	r.HeaderLinks = []model.Link{{Label: "PR #1", URL: "https://x/1"}, {Label: "Build", URL: "https://x/b"}}
	out := Render(r)
	if !strings.Contains(out, "[PR #1](https://x/1) · [Build](https://x/b)") {
		t.Fatalf("header links line missing:\n%s", out)
	}
}

func TestRenderStackAndResourceLinks(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].URL = "https://x/stack"
	r.Stacks[0].Changes[0].URL = "https://x/res"
	out := Render(r)
	if !strings.Contains(out, `<a href="https://x/stack">platform/nonprod</a>`) {
		t.Fatalf("stack link missing:\n%s", out)
	}
	if !strings.Contains(out, `<a href="https://x/res">google_storage_bucket.tfstate</a>`) {
		t.Fatalf("resource link missing:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run 'TestRenderHeaderLinks|TestRenderStackAndResourceLinks' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `renderHeader`, after the `### title (N stacks changed)` line, emit the links line when present:

```go
func renderHeader(b *strings.Builder, r model.Report) {
	fmt.Fprintf(b, "### %s  (%d stacks changed)\n\n", r.Title, changedStacks(r))
	if len(r.HeaderLinks) > 0 {
		parts := make([]string, 0, len(r.HeaderLinks))
		for _, l := range r.HeaderLinks {
			parts = append(parts, fmt.Sprintf("[%s](%s)", l.Label, l.URL))
		}
		fmt.Fprintf(b, "%s\n\n", strings.Join(parts, " · "))
	}
}
```

In `renderDetails`, wrap the stack name when `s.URL != ""`:

```go
		name := s.Name
		if s.URL != "" {
			name = fmt.Sprintf("<a href=%q>%s</a>", s.URL, s.Name)
		}
		summary := name
		if s.Class != nil {
			summary += " · " + s.Class.Label()
		}
		summary += " · " + changeWord(s.Counts)
```

In `resourceSummary`, link the address when `c.URL != ""`. Add a helper and use it for the `c.Address` in every branch:

```go
func resourceSummary(c model.Change) string {
	addr := c.Address
	if c.URL != "" {
		addr = fmt.Sprintf("<a href=%q>%s</a>", c.URL, c.Address)
	}
	n := len(c.Fields)
	switch {
	case c.Action == model.ActionForget:
		return fmt.Sprintf("⊘ %s · forgotten · %d attrs", addr, n)
	case c.Moved:
		s := fmt.Sprintf("↪ %s · moved from %s", addr, c.PreviousAddress)
		if n > 0 {
			s += fmt.Sprintf(", %d changed", n)
		}
		return s
	case c.Imported:
		s := fmt.Sprintf("⤓ %s · imported", addr)
		if c.ImportID != "" {
			s = fmt.Sprintf("⤓ %s · imported (id=%q)", addr, c.ImportID)
		}
		if n > 0 {
			s += fmt.Sprintf(", %d changed", n)
		}
		return s
	case c.Action == model.ActionAdd:
		return fmt.Sprintf("+ %s · %d attrs", addr, n)
	case c.Action == model.ActionDestroy:
		return fmt.Sprintf("- %s · %d attrs", addr, n)
	case c.Action == model.ActionReplace:
		return fmt.Sprintf("± %s · replace", addr)
	default:
		return fmt.Sprintf("~ %s · %d changed", addr, n)
	}
}
```

(Only the `c.Address` → `addr` substitution changes; the rest of `resourceSummary` is unchanged.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/render/ -v`
Expected: PASS (new + existing; `<a>` only appears when a URL is set, so existing link-less tests are unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/render/
git commit -m "render: header links line; <a> around stack name and resource address"
```

---

## Task 7: cmd — flags, index, resolution

**Files:**
- Modify: `cmd/tfstackplan/main.go`
- Test: `cmd/tfstackplan/main_test.go`

Wire it together: new flags, build a `source.Index` per stack (when `links.resource` is set), assemble vars, resolve URLs, and fill the model.

- [ ] **Step 1: Write the failing test**

Add to `cmd/tfstackplan/main_test.go` (reuses the `run` helper + `planJSON`/`cfgHCL` patterns there):

```go
func TestRunEmitsLinks(t *testing.T) {
	dir := t.TempDir()
	// a stack dir with the resource the plan changes
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte("resource \"google_project_iam_member\" \"editor\" {\n  role = \"x\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "plan.json")
	os.WriteFile(planPath, []byte(planJSON), 0o644) // planJSON changes google_project_iam_member.editor
	cfgPath := filepath.Join(dir, "cfg.hcl")
	os.WriteFile(cfgPath, []byte(`links {
  resource = "https://gh/o/r/blob/{sha}/{file}#L{line}"
  stack    = "https://gh/o/r/tree/{sha}/{stack_dir}"
  header {
    label = "PR #{pr}"
    url   = "https://gh/o/r/pull/{pr}"
  }
}`), 0o644)

	out, _, err := run(opts{
		stacks:   []string{"platform/nonprod:" + planPath},
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
	if !strings.Contains(out, "https://gh/o/r/blob/abc1234/main.tf#L1") {
		t.Fatalf("resource link missing:\n%s", out)
	}
	if !strings.Contains(out, "https://gh/o/r/tree/abc1234/.") && !strings.Contains(out, "tree/abc1234/") {
		t.Fatalf("stack link missing:\n%s", out)
	}
}
```

(Note: `planJSON` in `main_test.go` is a single `google_project_iam_member.editor` update; the stack dir's `main.tf` declares that resource at line 1, so `{file}=main.tf`, `{line}=1`. `stack_dir` is the plan dir relative to repoRoot = `.`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/tfstackplan -run TestRunEmitsLinks -v`
Expected: FAIL — `opts.repoRoot`/`linkVars` undefined.

- [ ] **Step 3: Implement**

Add fields to `opts`:

```go
	repoRoot string
	linkVars []string
```

Register flags in `main()` (with the others):

```go
	flag.StringVar(&o.repoRoot, "repo-root", ".", "repo root for computing link file paths")
	var lv stackFlags
	flag.Var(&lv, "link-var", "link template variable as key=value (repeatable)")
```

After `flag.Parse()`: `o.linkVars = lv` (mirror how `o.stacks = sf` is done).

In `run`, after `cfg` is loaded and before/while building `report`, prepare link vars and per-stack indexes. Add helpers and wire resolution:

```go
// baseVars holds run-level link variables (sha, pr, build_id, …) plus sha_short.
func baseVars(pairs []string) map[string]string {
	v := map[string]string{}
	for _, p := range pairs {
		if i := strings.IndexByte(p, '='); i > 0 {
			v[p[:i]] = p[i+1:]
		}
	}
	if sha := v["sha"]; len(sha) >= 7 {
		v["sha_short"] = sha[:7]
	}
	return v
}
```

(Import `strings`, `path/filepath`, `internal/links`, `internal/source` in main.go.)

In `run`, compute once:

```go
	base := baseVars(o.linkVars)
	if cfg.Links != nil {
		for _, l := range cfg.Links.Header {
			label := links.Resolve(l.Label, base)
			url := links.Resolve(l.URL, base)
			if url != "" {
				report.HeaderLinks = append(report.HeaderLinks, model.Link{Label: label, URL: url})
			}
		}
	}
```

For each stack `ref`, determine its source dir and index:

```go
		stackDir := ref.Dir
		if stackDir == "" {
			stackDir = filepath.Dir(ref.Plan)
		}
		stackRel := relSlash(o.repoRoot, stackDir)
		stackVars := mergeVars(base, map[string]string{"stack": ref.Name, "stack_dir": stackRel})
		if cfg.Links != nil {
			st.URL = links.Resolve(cfg.Links.Stack, stackVars)
		}
		var idx *source.Index
		if cfg.Links != nil && cfg.Links.Resource != "" {
			idx = source.Build(stackDir, o.repoRoot)
		}
```

When building each `model.Change` `ch`, resolve its URL:

```go
			if cfg.Links != nil {
				ch.URL = st.URL // default: fall back to the stack link
				if idx != nil {
					if loc, ok := idx.Lookup(rc.ModuleAddress, rc.Type, rc.Name); ok {
						rv := mergeVars(stackVars, map[string]string{
							"file": loc.File, "line": fmt.Sprintf("%d", loc.Line),
							"type": rc.Type, "name": rc.Name, "address": rc.Address, "module": rc.ModuleAddress,
						})
						if u := links.Resolve(cfg.Links.Resource, rv); u != "" {
							ch.URL = u
						}
					}
				}
			}
```

Add the small helpers:

```go
func mergeVars(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func relSlash(root, target string) string {
	ra, e1 := filepath.Abs(root)
	ta, e2 := filepath.Abs(target)
	if e1 != nil || e2 != nil {
		return ""
	}
	rel, err := filepath.Rel(ra, ta)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
```

(Place the resolution so `st.URL`/`stackVars`/`idx` are computed before the `for _, rc := range raw.Changes` loop, and `ch.URL` is set inside it next to the existing `ch.Fields = append(...)` logic.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/tfstackplan -run TestRunEmitsLinks -v`
Expected: PASS. Then `go build ./... && go test ./...` — whole module builds and passes (existing tests unaffected: with no `links` block, `cfg.Links` is nil and no URLs are set).

- [ ] **Step 5: Commit**

```bash
git add cmd/tfstackplan/
git commit -m "cmd: --repo-root/--link-var; build source index and resolve header/stack/resource links"
```

---

## Task 8: docs + example

**Files:**
- Modify: `README.md`, `docs/DESIGN.md`
- Modify: `cmd/tfstackplan/examples_test.go` (extend `TestStateOpsExample` or add a focused linked render assertion — keep goldens stable by NOT adding links to the existing golden runs, which pass no `--link-var`/`links{}`).

- [ ] **Step 1: README — add a "Links" subsection**

Under the classification/diff config docs, add a section documenting the `links {}` block, `--repo-root`, `--link-var`, the available template vars (`{file} {line} {stack} {stack_dir} {type} {name} {address} {module} {sha_short}` + any `--link-var` keys), and that unresolved resources fall back to the stack link. Include the example `links {}` block from the spec.

- [ ] **Step 2: DESIGN.md — document the feature**

Add a "Links" subsection (near rendering) summarizing: no source info in plan.json → parse `.tf` (`internal/source`) + `modules.json`; templates in HCL, values via `--link-var`; three levels; fallback. Add a row to the "Shipped since v1" list.

- [ ] **Step 3: Verify the existing goldens are unchanged**

Run: `go test ./cmd/tfstackplan -run TestExamples` and `go test ./cmd/tfstackplan -run TestStateOpsExample`
Expected: PASS without `-update` — the example runs pass no link config, so output is identical to the committed goldens.

- [ ] **Step 4: Full verification**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: gofmt empty; vet clean; all pass.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/DESIGN.md cmd/tfstackplan/
git commit -m "docs: document source-aware links"
```

---

## Self-Review

**Spec coverage:**
- HCL `links {}` (resource/stack/header) → Task 2. ✓
- `--repo-root`, `--link-var`, tool vars + run vars, missing-var omission → Tasks 1, 7. ✓
- `StackRef.dir` default to plan dir → Tasks 4, 7. ✓
- `internal/source` index (root + modules.json local, skip remote/.terraform, index-strip, first-decl-wins, outside-repo skip) → Task 3. ✓
- Module-key mapping `module.a.module.b`→`a.b` → Task 3. ✓
- Resource fallback to stack link → Task 7 (`ch.URL = st.URL` default). ✓
- model fields, render `<a>` + header line → Tasks 5, 6. ✓
- Goldens unchanged (no link config in example runs) → Task 8. ✓
- Testing per spec (links/source/config/render/cmd/e2e) → Tasks 1–7. ✓

**Placeholder scan:** none — every step has concrete code/commands.

**Type consistency:** `links.Resolve(string, map[string]string) string`; `config.LinksConfig{Resource, Stack, Header []HeaderLink{Label,URL}}` + `Config.Links`; `source.Build(dir, repoRoot) *Index`, `source.Loc{File,Line}`, `(*Index).Lookup(moduleAddress, typ, name) (Loc,bool)`, `moduleKey`; `plan.RawChange.Name/.ModuleAddress`; `manifest.StackRef.Dir`; `model.Link{Label,URL}`, `Report.HeaderLinks`, `Stack.URL`, `Change.URL`; `opts.repoRoot/.linkVars`; helpers `baseVars`/`mergeVars`/`relSlash` — used consistently across tasks.
