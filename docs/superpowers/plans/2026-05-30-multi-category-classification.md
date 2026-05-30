# Multi-category Classification + Run Summary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make classification multi-label — a stack carries the set of categories it matched (every rule that fires, not just the first) — and emit a run summary that unions each category's attributes across the run.

**Architecture:** `classify.Classify` returns an ordered `[]Category` and a new `classify.Summarize` unions them across stacks. `model.Stack` carries `[]Class`; `render` shows a `Categories` column with a default-badge fallback. `cmd` reshapes the `--emit-classification-json` sidecar to `{stacks, summary}`. No HCL change.

**Tech Stack:** Go 1.23, standard library. No new dependencies (the `model` import is *removed* from `classify`).

**Spec:** `docs/superpowers/specs/2026-05-30-multi-category-classification-design.md`.

---

## File Structure

| File | Change |
|------|--------|
| `internal/classify/classify.go` | `Result`→`Category`; `Classify` returns `[]Category` (drop `def`); add `Summarize` + `mergeAttrs`; drop `model` import. |
| `internal/classify/classify_test.go` | Rewrite for multi-match + add `Summarize` tests. |
| `internal/model/model.go` | `Stack.Class *Class` → `Stack.Categories []Class`; add `Report.Default Class`. |
| `internal/render/render.go` | `Class` column → `Categories`; `categoriesCell` helper with default fallback; details suffix. |
| `internal/render/render_test.go` | Update `sampleReport`; rename/extend the column test; fix the unclassified test. |
| `cmd/tfstackplan/main.go` | Wire `[]Category` into `st.Categories` + `report.Default`; build `{stacks, summary}` sidecar via `Summarize`. |
| `cmd/tfstackplan/main_test.go` | Update the sidecar e2e to the new shape. |
| `cmd/tfstackplan/examples_test.go` | Make one fixture stack multi-category; regenerate goldens. |
| `README.md`, `docs/DESIGN.md` | Document multi-category + the new sidecar/summary shape. |

**Compile coupling:** `classify` is imported by `cmd`, and `model` by `render`+`cmd`, so changing their APIs breaks downstream packages until the `cmd` task. Each task below verifies **its own package(s)**; the full `go build ./... && go test ./...` goes green only at Task 4 (cmd). This mirrors how compile-coupled refactors are sequenced — intermediate full-builds are red by design.

---

### Task 1: `internal/classify` — multi-match + Summarize

**Files:**
- Modify: `internal/classify/classify.go`
- Modify: `internal/classify/classify_test.go`

- [ ] **Step 1: Rewrite the tests for the new API**

Replace the entire contents of `internal/classify/classify_test.go` with:

```go
package classify

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
)

func stack(changes ...plan.RawChange) plan.RawStack {
	return plan.RawStack{Name: "s", Changes: changes}
}

// find returns the category with the given name, or false.
func find(cats []Category, name string) (Category, bool) {
	for _, c := range cats {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}

func TestAllMatchingRulesFire(t *testing.T) {
	rules := []Rule{
		{Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1},
		{Name: "destructive", Icon: "💣", Actions: []string{"delete"}, MinCount: 1},
	}
	// A deleted IAM member matches BOTH rules.
	s := stack(plan.RawChange{Type: "google_project_iam_member", Actions: []string{"delete"}})
	got := Classify(s, rules)
	if len(got) != 2 {
		t.Fatalf("expected 2 categories, got %d: %+v", len(got), got)
	}
	// Rule order is preserved.
	if got[0].Name != "iam" || got[1].Name != "destructive" {
		t.Fatalf("category order = [%q %q], want [iam destructive]", got[0].Name, got[1].Name)
	}
}

func TestNoMatchYieldsEmpty(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}}), rules)
	if len(got) != 0 {
		t.Fatalf("no match should yield empty slice, got %+v", got)
	}
}

func TestActionsAndMinCount(t *testing.T) {
	rules := []Rule{{Name: "destructive", Actions: []string{"delete"}, MinCount: 2}}
	if cats := Classify(stack(plan.RawChange{Type: "x", Actions: []string{"delete"}}), rules); len(cats) != 0 {
		t.Fatalf("one delete must not meet min_count 2, got %+v", cats)
	}
	two := stack(
		plan.RawChange{Type: "x", Actions: []string{"delete"}},
		plan.RawChange{Type: "y", Actions: []string{"delete"}},
	)
	if _, ok := find(Classify(two, rules), "destructive"); !ok {
		t.Fatal("two deletes should meet min_count 2")
	}
}

func TestEmitAttributesFromMatchedChangesOnly(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"update"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	iam, ok := find(Classify(s, rules), "iam")
	if !ok {
		t.Fatal("expected iam category")
	}
	if len(iam.Attributes["project"]) != 1 || iam.Attributes["project"][0] != "p1" {
		t.Fatalf("project = %v, want [p1] (bucket's p2 must NOT appear)", iam.Attributes["project"])
	}
}

func TestEmitAttributesDedupeAndSort(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	iam, _ := find(Classify(s, rules), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1", "p2"}) {
		t.Fatalf("project = %v, want [p1 p2]", iam.Attributes["project"])
	}
}

func TestEmitAttributesNilWhenNoneConfigured(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	iam, _ := find(Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules), "iam")
	if iam.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no emit_attributes", iam.Attributes)
	}
}

func TestEmitAttributesNilWhenNoValuesFound(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	iam, ok := find(Classify(
		stack(plan.RawChange{Type: "google_organization_iam_binding", Actions: []string{"create"}, Raw: map[string]any{"role": "roles/x"}}),
		rules), "iam")
	if !ok {
		t.Fatal("expected iam category")
	}
	if iam.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no project values", iam.Attributes)
	}
}

func TestBelowMinCountDoesNotFire(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 2,
		EmitAttributes: []string{"project"},
	}}
	got := Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules)
	if len(got) != 0 {
		t.Fatalf("rule below MinCount must not fire, got %+v", got)
	}
}

func TestSummarizeUnionsAcrossStacks(t *testing.T) {
	rules := []Rule{
		{Name: "iam", Icon: "🔐"},
		{Name: "sql-server", Icon: "🗄"},
	}
	perStack := [][]Category{
		{{Name: "iam", Icon: "🔐", Attributes: map[string][]string{"project": {"p1", "p2"}}}},
		{
			{Name: "iam", Icon: "🔐", Attributes: map[string][]string{"project": {"p2", "p3"}}},
			{Name: "sql-server", Icon: "🗄", Attributes: map[string][]string{"instance": {"db1"}}},
		},
		{}, // a stack that matched nothing contributes nothing
	}
	got := Summarize(perStack, rules)
	if len(got) != 2 {
		t.Fatalf("expected 2 summary categories, got %d: %+v", len(got), got)
	}
	// Order follows rules: iam, then sql-server.
	if got[0].Name != "iam" || got[1].Name != "sql-server" {
		t.Fatalf("summary order = [%q %q], want [iam sql-server]", got[0].Name, got[1].Name)
	}
	if !reflect.DeepEqual(got[0].Attributes["project"], []string{"p1", "p2", "p3"}) {
		t.Fatalf("iam project union = %v, want [p1 p2 p3]", got[0].Attributes["project"])
	}
	if !reflect.DeepEqual(got[1].Attributes["instance"], []string{"db1"}) {
		t.Fatalf("sql-server instance = %v, want [db1]", got[1].Attributes["instance"])
	}
}

func TestSummarizeEmpty(t *testing.T) {
	if got := Summarize([][]Category{{}, {}}, []Rule{{Name: "iam"}}); len(got) != 0 {
		t.Fatalf("no categories should summarize to empty, got %+v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/classify/`
Expected: FAIL to compile — `Classify` still takes 3 args / returns `Result`; `Summarize` undefined.

- [ ] **Step 3: Rewrite `classify.go` for the new API**

In `internal/classify/classify.go`:

(a) Remove the `model` import (the line `"github.com/Fluent-Health/terraform-stack-plan/internal/model"`). The remaining imports (`encoding/json`, `fmt`, `regexp`, `sort`, `strconv`, `plan`) stay.

(b) Replace the `Result` type and the `Classify` function (the block from `// Result is the outcome…` through the end of `Classify`) with:

```go
// Category is one matched rule's outcome: its name, icon, and — for the rule's
// EmitAttributes — the sorted-unique non-null values across the changes it
// matched. Attributes is nil when nothing was emitted.
type Category struct {
	Name       string
	Icon       string
	Attributes map[string][]string
}

// Classify returns a Category for every rule that matches enough changes, in
// rule order. The slice is empty when no rule fires — the caller supplies the
// display fallback. Rules are independent; there is no first-match-wins.
func Classify(s plan.RawStack, rules []Rule) []Category {
	var cats []Category
	for _, r := range rules {
		min := r.MinCount
		if min < 1 {
			min = 1
		}
		var matched []plan.RawChange
		for _, c := range s.Changes {
			if ruleMatchesChange(r, c) {
				matched = append(matched, c)
			}
		}
		if len(matched) >= min {
			cats = append(cats, Category{
				Name:       r.Name,
				Icon:       r.Icon,
				Attributes: extract(matched, r.EmitAttributes),
			})
		}
	}
	return cats
}

// Summarize unions categories across stacks: for each rule (in rules order)
// that any stack matched, it returns one Category whose Attributes merge every
// matching stack's values (sorted-unique per key). Categories no stack matched
// are omitted.
func Summarize(perStack [][]Category, rules []Rule) []Category {
	agg := map[string]*Category{}
	for _, cats := range perStack {
		for _, c := range cats {
			a, ok := agg[c.Name]
			if !ok {
				a = &Category{Name: c.Name, Icon: c.Icon}
				agg[c.Name] = a
			}
			a.Attributes = mergeAttrs(a.Attributes, c.Attributes)
		}
	}
	var out []Category
	for _, r := range rules {
		if a, ok := agg[r.Name]; ok {
			out = append(out, *a)
		}
	}
	return out
}

// mergeAttrs returns the per-key sorted-unique union of two attribute maps,
// or nil when the result is empty.
func mergeAttrs(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, m := range []map[string][]string{a, b} {
		for k, vs := range m {
			seen := map[string]struct{}{}
			for _, x := range out[k] {
				seen[x] = struct{}{}
			}
			for _, v := range vs {
				if _, dup := seen[v]; !dup {
					seen[v] = struct{}{}
					out[k] = append(out[k], v)
				}
			}
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

Leave `extract`, `scalarString`, `ruleMatchesChange`, and `contains` exactly as they are.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/classify/`
Expected: PASS (all tests). Also run `go vet ./internal/classify/` and `gofmt -l internal/classify/` (no output).

(Note: `go build ./...` is expected to FAIL now — `cmd` still calls the old `Classify`. That is fixed in Task 4.)

- [ ] **Step 5: Commit**

```bash
git add internal/classify/
git commit -m "classify: multi-label Classify returns all matching rules; add Summarize"
```

---

### Task 2: `internal/model` + `internal/render` — Categories column

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/render/render.go`
- Modify: `internal/render/render_test.go`

- [ ] **Step 1: Update the model types**

In `internal/model/model.go`:

(a) In the `Stack` struct, replace the `Class *Class` field with:
```go
	Categories []Class // matched categories in rule order; empty → render the default
```

(b) In the `Report` struct, update the `Classified` comment and add a `Default` field:
```go
	Classified  bool  // whether to show the Categories column
	Default     Class // fallback badge shown for a stack that matched no category
```
(Place `Default` next to `Classified`.)

- [ ] **Step 2: Update the render tests (will fail to compile until Step 3)**

In `internal/render/render_test.go`, replace `sampleReport` and the two class tests:

```go
func sampleReport() model.Report {
	return model.Report{
		Title:      "Terraform plan — nonprod",
		Marker:     "tfstackplan:nonprod",
		Classified: true,
		Default:    model.Class{Name: "safe", Icon: "✅"},
		Stacks: []model.Stack{
			{
				Name:       "platform/nonprod",
				Counts:     model.Counts{Change: 1},
				Categories: []model.Class{{Name: "iam", Icon: "🔐"}, {Name: "destructive", Icon: "💣"}},
				Changes: []model.Change{{
					Address: "google_storage_bucket.tfstate",
					Type:    "google_storage_bucket",
					Action:  model.ActionChange,
					Fields: []model.Field{
						{Name: "labels", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "labels.team", New: `"platform"`}}},
						{Name: "retention_days", Leaves: []model.Leaf{{Op: model.OpChange, Path: "retention_days", Old: "7", New: "30"}}},
					},
				}},
			},
			{Name: "svc/dev", Counts: model.Counts{Add: 2}}, // no Categories → renders the default
		},
	}
}

func TestRenderCategoriesColumnAndDetails(t *testing.T) {
	out := Render(sampleReport())
	if !strings.Contains(out, "Categories") {
		t.Fatalf("expected Categories column header:\n%s", out)
	}
	if !strings.Contains(out, "🔐 iam") || !strings.Contains(out, "💣 destructive") {
		t.Fatalf("expected both category badges in the multi-category row:\n%s", out)
	}
	if !strings.Contains(out, "✅ safe") {
		t.Fatalf("expected the default badge for the no-category stack:\n%s", out)
	}
	if !strings.Contains(out, "<details><summary>platform/nonprod") {
		t.Fatalf("expected details for changed stack:\n%s", out)
	}
	if !strings.Contains(out, "```diff") {
		t.Fatalf("expected diff fence:\n%s", out)
	}
}

func TestRenderNoCategoriesColumnWhenUnclassified(t *testing.T) {
	r := sampleReport()
	r.Classified = false
	for i := range r.Stacks {
		r.Stacks[i].Categories = nil
	}
	out := Render(r)
	if strings.Contains(out, "Categories") {
		t.Fatalf("Categories column must be absent when unclassified:\n%s", out)
	}
}
```

- [ ] **Step 3: Update `render.go`**

In `internal/render/render.go`:

(a) In `renderTable`, change the header append:
```go
	if r.Classified {
		headers = append(headers, "Categories")
	}
```

(b) In the alignment switch, add `"Categories"` to the left-aligned case:
```go
		case "Stack", "Categories":
			aligns[i] = "---"
```

(c) Replace the per-row class cell block:
```go
		if r.Classified {
			cells = append(cells, categoriesCell(s, r))
		}
```

(d) In `renderDetails`, replace the `if s.Class != nil { summary += " · " + s.Class.Label() }` block with:
```go
		if r.Classified {
			summary += " · " + categoriesCell(s, r)
		}
```

(e) Add the helper (next to `renderTable`):
```go
// categoriesCell renders a stack's category badges joined by two spaces, or the
// report's default badge when the stack matched no category.
func categoriesCell(s model.Stack, r model.Report) string {
	if len(s.Categories) == 0 {
		return r.Default.Label()
	}
	parts := make([]string, len(s.Categories))
	for i, c := range s.Categories {
		parts[i] = c.Label()
	}
	return strings.Join(parts, "  ")
}
```

- [ ] **Step 4: Run the render tests**

Run: `go test ./internal/render/`
Expected: PASS. Also `go vet ./internal/render/` and `gofmt -l internal/render/ internal/model/` (no output).

(`go build ./...` still fails — `cmd` is fixed in Task 4.)

- [ ] **Step 5: Commit**

```bash
git add internal/model/ internal/render/
git commit -m "model,render: stack carries a set of categories; Categories column with default fallback"
```

---

### Task 3: `cmd/tfstackplan` — wire categories + reshape sidecar

**Files:**
- Modify: `cmd/tfstackplan/main.go`
- Modify: `cmd/tfstackplan/main_test.go`
- Modify: `cmd/tfstackplan/examples_test.go`

- [ ] **Step 1: Rewrite the sidecar types + helpers in `main.go`**

In `cmd/tfstackplan/main.go`, replace the `classEntry` type definition and the `nilable` function region with:

```go
type categoryEntry struct {
	Category   string              `json:"category"`
	Icon       *string             `json:"icon"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

type stackEntry struct {
	Categories []categoryEntry `json:"categories"`
}

type sidecarDoc struct {
	Stacks  map[string]stackEntry `json:"stacks"`
	Summary struct {
		Categories []categoryEntry `json:"categories"`
	} `json:"summary"`
}

// toEntries maps classify categories to their JSON form. Always returns a
// non-nil slice so a category-less stack marshals as [] rather than null.
func toEntries(cats []classify.Category) []categoryEntry {
	out := make([]categoryEntry, 0, len(cats))
	for _, c := range cats {
		out = append(out, categoryEntry{Category: c.Name, Icon: nilable(c.Icon), Attributes: c.Attributes})
	}
	return out
}

// toClasses maps classify categories to render-model classes (name+icon only).
func toClasses(cats []classify.Category) []model.Class {
	out := make([]model.Class, len(cats))
	for i, c := range cats {
		out[i] = model.Class{Name: c.Name, Icon: c.Icon}
	}
	return out
}

func nilable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
```

- [ ] **Step 2: Wire categories into `run`**

In `cmd/tfstackplan/main.go`, inside `run`:

(a) Where the report is constructed, set the default when classified. Replace the `report := model.Report{…}` line with:
```go
	report := model.Report{Title: o.title, Marker: o.marker, Classified: classified}
	if classified {
		report.Default = cfg.Classification.Default
	}
```

(b) Replace the sidecar accumulator declaration `sidecar := map[string]classEntry{}` with:
```go
	doc := sidecarDoc{Stacks: map[string]stackEntry{}}
	var allCats [][]classify.Category
```

(c) Replace the per-stack classification block:
```go
		if classified {
			res := classify.Classify(raw, cfg.Classification.Rules, cfg.Classification.Default)
			st.Class = &res.Class
			sidecar[ref.Name] = classEntry{Class: res.Class.Name, Icon: nilable(res.Class.Icon), Attributes: res.Attributes}
		}
```
with:
```go
		if classified {
			cats := classify.Classify(raw, cfg.Classification.Rules)
			st.Categories = toClasses(cats)
			allCats = append(allCats, cats)
			doc.Stacks[ref.Name] = stackEntry{Categories: toEntries(cats)}
		}
```

(d) Replace the sidecar marshal/write block:
```go
	if o.classJSON != "" && classified {
		data, err := json.MarshalIndent(sidecar, "", "  ")
		if err != nil {
			return "", false, err
		}
		if err := os.WriteFile(o.classJSON, data, 0o644); err != nil {
			return "", false, err
		}
	}
```
with:
```go
	if o.classJSON != "" && classified {
		doc.Summary.Categories = toEntries(classify.Summarize(allCats, cfg.Classification.Rules))
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return "", false, err
		}
		if err := os.WriteFile(o.classJSON, data, 0o644); err != nil {
			return "", false, err
		}
	}
```

Confirm `internal/model` is imported in `main.go` (it is — used elsewhere). Run `go build ./cmd/tfstackplan/` to check it compiles before touching tests.

- [ ] **Step 3: Update the sidecar e2e test**

In `cmd/tfstackplan/main_test.go`:

(a) `TestRunPlansDir` reads the sidecar as `map[string]struct{Class,Icon}` — update it to the new shape. Replace its sidecar-decode-and-assert tail (from `var got map[string]struct {` to the end of the function) with:
```go
	var got sidecarDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	cats := got.Stacks["platform/nonprod"].Categories
	if len(cats) != 1 || cats[0].Category != "iam" {
		t.Fatalf("platform/nonprod categories = %+v, want one iam", cats)
	}
}
```

(b) Replace `TestRunEmitsClassificationAttributes` entirely with a multi-category version:
```go
func TestRunEmitsClassificationAttributes(t *testing.T) {
	dir := t.TempDir()
	plansDir := filepath.Join(dir, "out")

	// platform/nonprod: a deleted IAM member → matches BOTH iam and destructive.
	iamPlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.a","type":"google_project_iam_member","name":"a",
	     "change":{"actions":["delete"],"before":{"role":"roles/x","project":"fh-host-nonprod"},"after":null,
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"google_project_iam_member.b","type":"google_project_iam_member","name":"b",
	     "change":{"actions":["delete"],"before":{"role":"roles/y","project":"fh-svc-dev"},"after":null,
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	// data/warehouse: a single delete → destructive only, no iam, no project.
	safePlan := `{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_storage_bucket.b","type":"google_storage_bucket","name":"b",
	     "change":{"actions":["delete"],"before":{"name":"bkt"},"after":null,
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`
	cfg := `
classification {
  default { name = "safe" icon = "✅" }
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }
  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
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
	var got sidecarDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	// platform/nonprod carries both categories, in rule order (iam, destructive).
	pcats := got.Stacks["platform/nonprod"].Categories
	if len(pcats) != 2 || pcats[0].Category != "iam" || pcats[1].Category != "destructive" {
		t.Fatalf("platform/nonprod categories = %+v, want [iam destructive]", pcats)
	}
	if iam := pcats[0]; len(iam.Attributes["project"]) != 2 ||
		iam.Attributes["project"][0] != "fh-host-nonprod" || iam.Attributes["project"][1] != "fh-svc-dev" {
		t.Fatalf("iam project = %v, want [fh-host-nonprod fh-svc-dev]", iam.Attributes["project"])
	}

	// data/warehouse is destructive only.
	dcats := got.Stacks["data/warehouse"].Categories
	if len(dcats) != 1 || dcats[0].Category != "destructive" {
		t.Fatalf("data/warehouse categories = %+v, want [destructive]", dcats)
	}

	// Summary unions both categories present across the run, in rule order.
	if len(got.Summary.Categories) != 2 ||
		got.Summary.Categories[0].Category != "iam" || got.Summary.Categories[1].Category != "destructive" {
		t.Fatalf("summary categories = %+v, want [iam destructive]", got.Summary.Categories)
	}
	if proj := got.Summary.Categories[0].Attributes["project"]; len(proj) != 2 {
		t.Fatalf("summary iam project union = %v, want 2 values", proj)
	}
	// destructive declares no emit_attributes → its attributes field is omitted.
	if got.Summary.Categories[1].Attributes != nil {
		t.Fatalf("destructive must have no attributes, got %v", got.Summary.Categories[1].Attributes)
	}
}
```

(c) `TestRunEmptyPlansDir` asserts the sidecar equals `{}`. Update that assertion to the new empty shape. Replace:
```go
	if strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("sidecar should be empty object, got: %s", data)
	}
```
with:
```go
	var got sidecarDoc
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Stacks) != 0 || len(got.Summary.Categories) != 0 {
		t.Fatalf("empty run should have no stacks and no summary categories, got: %s", data)
	}
```

- [ ] **Step 4: Make one example fixture stack multi-category**

In `cmd/tfstackplan/examples_test.go`, in `exampleStacks`, the `data/warehouse` stack is currently all `del(...)` of non-IAM resources. Change its first entry from a bigquery delete to a deleted IAM member so the stack matches **both** `destructive` (delete) and `iam` (the `*_iam_member` type), demonstrating multi-category. Replace:
```go
		{"data/warehouse", []change{
			del("google_bigquery_dataset.legacy_events", "google_bigquery_dataset"),
```
with:
```go
		{"data/warehouse", []change{
			del("google_project_iam_member.legacy_admins", "google_project_iam_member"),
```
(Leave the other `data/warehouse` entries unchanged. This keeps the destroy count identical — one delete swapped for another — and makes `data/warehouse` render `🔐 iam  💣 destructive`.)

- [ ] **Step 5: Build, run cmd tests (expect golden mismatch), then regenerate**

Run: `go build ./... && go test ./cmd/tfstackplan/`
Expected: the `TestRun*` tests PASS; `TestExamples`/`TestStateOpsExample` FAIL as stale (the `Class` column is now `Categories`, and `data/warehouse` shows two badges). If any `TestRun*` fails, fix it before regenerating — do not mask a logic error with `-update`.

Then regenerate and re-run:
```bash
go test ./cmd/tfstackplan/ -update
go test ./...
```
Expected: all PASS (full repo green for the first time since Task 1).

- [ ] **Step 6: Inspect the regenerated goldens**

Run: `git diff examples/big-plan.md | grep -A2 -B2 "Categories\|data/warehouse"` and skim.
Expected: column header is now `Categories`; the `data/warehouse` row shows `🔐 iam  💣 destructive`; other rows unchanged except the header. Confirm no unintended content change.

- [ ] **Step 7: Commit**

```bash
git add cmd/tfstackplan/ examples/
git commit -m "cmd: emit {stacks,summary} sidecar with multi-category results; regenerate goldens"
```

---

### Task 4: Documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/DESIGN.md`

- [ ] **Step 1: Update the README Classification section**

In `README.md`, find the `## Classification (optional)` section and update the prose + examples to multi-category:

- Change the "first rule that fires sets the class" description to: **every** rule that fires contributes a category; a stack carries the set of matched categories (rule order = badge order); `default` is shown only when nothing matched.
- Update the "evaluate top-to-bottom in source order, first hit wins" line — order now controls **display order of badges**, not precedence.
- Update the **Sidecar JSON** subsection to the new shape. The per-entry key is `category` (matching the implementation's JSON tag). Replace the old `{ "<stack>": { "class": …, "icon": … } }` example with:
```json
{
  "stacks": {
    "platform/nonprod": { "categories": [
      { "category": "iam",         "icon": "🔐", "attributes": { "project": ["fh-host-nonprod"] } },
      { "category": "destructive", "icon": "💣" }
    ]},
    "data/warehouse": { "categories": [] }
  },
  "summary": { "categories": [
    { "category": "iam",         "icon": "🔐", "attributes": { "project": ["fh-host-nonprod", "fh-svc-dev"] } },
    { "category": "destructive", "icon": "💣" }
  ]}
}
```
- Document `summary`: every category present across the run, each with the per-key sorted-unique union of its attributes — the data a CI gate consumes directly.
- Note that the rendered column is **Categories** and a stack with no match shows the `default` badge; `default`/`safe` never appears in the sidecar or summary.
- Update the "Emitting matched attributes" subsection so its sidecar snippet matches the new nesting (attributes now live inside each category entry under `stacks.<name>.categories[]` and `summary.categories[]`).

- [ ] **Step 2: Update the README "What it looks like" column header**

The hand-written example table near the top uses a `Class` column header. Change that header to `Categories` for consistency (the illustrative rows can stay single-badge; no need to fabricate multi-badge rows, though one is fine).

- [ ] **Step 3: Update docs/DESIGN.md**

In `docs/DESIGN.md`:
- In the Classification section, change the single-class / first-match description to multi-category (all matching rules fire; stack carries a set; order = display; `default` = empty-set fallback, render-only).
- Update the "Sidecar JSON" subsection to the `{stacks, summary}` shape and describe the summary union.
- Update any reference to a `Class` column to `Categories`.

- [ ] **Step 4: Verify no stale single-class references**

Run: `grep -rni "first rule\|first hit\|first.match\|single class\|\"class\":" README.md docs/DESIGN.md`
Expected: no matches describing the old single-class behavior. Fix any that remain.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/DESIGN.md docs/superpowers/
git commit -m "docs: multi-category classification + {stacks,summary} sidecar"
```

---

## Self-Review

**Spec coverage** (against `2026-05-30-multi-category-classification-design.md`):
- `Classify` returns all matches, rule order, drop `def` → Task 1 + `TestAllMatchingRulesFire`/`TestNoMatchYieldsEmpty`. ✓
- `Summarize` union per key, rule order → Task 1 + `TestSummarizeUnionsAcrossStacks`. ✓
- `default` render-only, absent from sidecar/summary → Task 2 (`categoriesCell` fallback) + Task 3 (`safe` stack → `categories: []`, summary excludes it) + `TestRunEmitsClassificationAttributes`. ✓
- Sidecar `{stacks, summary}` shape → Task 3 types + e2e. ✓
- `Categories` column, all badges, default when empty → Task 2 + `TestRenderCategoriesColumnAndDetails`. ✓
- No HCL change → config untouched (only `classify`/`model`/`render`/`cmd`). ✓
- Empty run → `{"stacks":{},"summary":{"categories":[]}}` → Task 3 `TestRunEmptyPlansDir`. ✓
- Multi-category demonstrated in goldens → Task 3 Step 4. ✓

**Placeholder scan:** none. The README JSON example (Task 4 Step 1) uses the `category` key, matching the implementation's struct tag.

**Type consistency:** `classify.Category{Name,Icon,Attributes}` (Task 1) ↔ `toClasses`/`toEntries` (Task 3) ↔ `model.Stack.Categories []Class` + `Report.Default` (Task 2) ↔ `categoriesCell` (Task 2). `Classify(raw, rules)` 2-arg form used consistently in Task 3. `Summarize(allCats, rules)` matches the Task 1 signature. Sidecar JSON field is `category` everywhere.

---

## Out of scope (per spec)
- Aggregate counts in the summary (deferred, additive later).
- Per-category stack lists in the summary.
- Any gating/PAM/VCS vocabulary in the tool.
- HCL schema changes.
- The infra-side consumer rewrite (separate repo/track).
