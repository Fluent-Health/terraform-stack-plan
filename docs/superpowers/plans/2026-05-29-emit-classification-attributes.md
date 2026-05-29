# Emit classification attributes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a classification `rule`/`preset` declare `emit_attributes`; the `--emit-classification-json` sidecar then carries the sorted-unique non-null values of those attributes from the firing rule's matched changes.

**Architecture:** `plan.Parse` retains a per-change map of top-level scalar attributes (`RawChange.Raw`), populated for every change and skipping sensitive values — because the reduced `Attrs` list drops *unchanged* attributes, and we must read e.g. `project` even on an in-place update. `classify.Classify` returns a richer `Result{Class, Attributes}`, extracting the firing rule's `EmitAttributes` from its matched changes (after→before, nil-drop, sort-unique). `config`/`presets` carry the new list through; `cmd` serializes it into the sidecar under an `omitempty` `attributes` key.

**Tech Stack:** Go, `github.com/hashicorp/terraform-json` (plan parsing), `github.com/hashicorp/hcl/v2` (policy file), standard `testing`.

**Spec:** `docs/superpowers/specs/2026-05-29-emit-classification-attributes-design.md`

---

## File Structure

| File | Responsibility | Action |
|------|----------------|--------|
| `internal/plan/plan.go` | Add `RawChange.Raw map[string]any` + `rawScalars`/`isScalar` helpers; populate in `Parse`. | Modify |
| `internal/plan/plan_test.go` | Test `Raw` includes unchanged top-level scalars, excludes nested + sensitive. | Modify |
| `internal/classify/classify.go` | Add `Rule.EmitAttributes`, `Result` type; change `Classify` to return `Result`; add `extract`/`scalarString`. | Modify |
| `internal/classify/classify_test.go` | Update existing tests to new return; add extraction tests. | Modify |
| `internal/presets/presets.go` | `Get` accepts `emitAttributes`, sets it on the rule. | Modify |
| `internal/presets/presets_test.go` | Update `Get` calls; assert propagation. | Modify |
| `internal/config/config.go` | Parse `emit_attributes` on `preset` + `rule` bodies; pass through. | Modify |
| `internal/config/config_test.go` + `testdata/emit.hcl` | Parse-test on both block kinds. | Modify / Create |
| `cmd/tfstackplan/main.go` | `classEntry.Attributes` (`omitempty`); update `Classify` call-site. | Modify |
| `cmd/tfstackplan/main_test.go` | End-to-end: attributes emitted for IAM stack, omitted for safe. | Modify |
| `README.md` | Document `emit_attributes` + sidecar `attributes`. | Modify |

---

## Task 1: Retain top-level scalar attributes in `plan`

**Files:**
- Modify: `internal/plan/plan.go` (struct `RawChange` ~line 27; `Parse` loop ~line 101; new helpers near `truthy` ~line 226)
- Test: `internal/plan/plan_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/plan/plan_test.go`:

```go
func TestParseRawRetainsUnchangedScalarsSkipsSensitiveAndNested(t *testing.T) {
	// In-place update: only "role" changes; "project" is unchanged; "secret"
	// is sensitive; "labels" is a nested object. Raw must keep role+project
	// (scalars), drop secret (sensitive) and labels (non-scalar).
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x",
	     "change":{"actions":["update"],
	       "before":{"role":"roles/viewer","project":"p1","secret":"old","labels":{"a":"b"}},
	       "after":{"role":"roles/editor","project":"p1","secret":"new","labels":{"a":"b"}},
	       "after_unknown":{},
	       "before_sensitive":{"secret":true},"after_sensitive":{"secret":true}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(rs.Changes))
	}
	raw := rs.Changes[0].Raw
	if raw["project"] != "p1" {
		t.Errorf("Raw[project] = %v, want p1 (must survive even though unchanged)", raw["project"])
	}
	if raw["role"] != "roles/editor" {
		t.Errorf("Raw[role] = %v, want roles/editor (after wins)", raw["role"])
	}
	if _, ok := raw["secret"]; ok {
		t.Error("Raw must not include sensitive attribute 'secret'")
	}
	if _, ok := raw["labels"]; ok {
		t.Error("Raw must not include non-scalar attribute 'labels'")
	}
}

func TestParseRawPrefersAfterFallsBackToBefore(t *testing.T) {
	// Delete: after is null, so Raw comes from before.
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"google_project_iam_member.x","type":"google_project_iam_member","name":"x",
	     "change":{"actions":["delete"],
	       "before":{"project":"p9"},"after":null,
	       "before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Changes[0].Raw["project"] != "p9" {
		t.Errorf("Raw[project] = %v, want p9 (from before on delete)", rs.Changes[0].Raw["project"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -run TestParseRaw -v`
Expected: FAIL — `rs.Changes[0].Raw` undefined (field does not exist yet).

- [ ] **Step 3: Add the `Raw` field**

In `internal/plan/plan.go`, add to the `RawChange` struct (after `Attrs []RawAttr` ~line 35):

```go
	// Raw holds top-level scalar attributes (string/number/bool), after over
	// before, sensitive values skipped. Retained for classification attribute
	// extraction, which must see attributes even when they did not change.
	Raw map[string]any
```

- [ ] **Step 4: Populate `Raw` in `Parse`**

In `internal/plan/plan.go`, inside the `Parse` loop, in the `RawChange{...}` literal (~line 101) add the field:

```go
		ch := RawChange{
			Address:       rc.Address,
			Type:          rc.Type,
			Actions:       toStrings(act),
			Action:        bucket,
			Moved:         moved,
			Imported:      imported,
			Name:          rc.Name,
			ModuleAddress: rc.ModuleAddress,
			Raw:           rawScalars(rc.Change),
		}
```

- [ ] **Step 5: Add the helpers**

In `internal/plan/plan.go`, add near `truthy` (end of file). Add `"encoding/json"` to the import block:

```go
// rawScalars returns the change's top-level scalar attributes (string, bool,
// number), preferring after over before and skipping any flagged sensitive.
// Used by classification attribute extraction, which must see an attribute even
// when it did not change (changedAttrs keeps only differing values).
func rawScalars(c *tfjson.Change) map[string]any {
	after, _ := c.After.(map[string]any)
	before, _ := c.Before.(map[string]any)
	afterSens, _ := c.AfterSensitive.(map[string]any)
	beforeSens, _ := c.BeforeSensitive.(map[string]any)

	out := map[string]any{}
	put := func(src, sens map[string]any) {
		for k, v := range src {
			if _, ok := out[k]; ok {
				continue // after already won
			}
			if truthy(sens[k]) {
				continue // never surface a sensitive value
			}
			if isScalar(v) {
				out[k] = v
			}
		}
	}
	put(after, afterSens)
	put(before, beforeSens)
	if len(out) == 0 {
		return nil
	}
	return out
}

// isScalar reports whether v is a JSON scalar we can stringify for the sidecar.
func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, float64, json.Number:
		return true
	default:
		return false
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/plan/ -v`
Expected: PASS (new tests + all existing plan tests).

- [ ] **Step 7: Commit**

```bash
git add internal/plan/plan.go internal/plan/plan_test.go
git commit -m "plan: retain top-level scalar attrs on RawChange (Raw)"
```

---

## Task 2: `Result` + attribute extraction in `classify`

**Files:**
- Modify: `internal/classify/classify.go` (struct `Rule` ~line 14; `Classify` ~line 24; new `extract`/`scalarString`)
- Test: `internal/classify/classify_test.go`

- [ ] **Step 1: Update existing tests to the new return type**

In `internal/classify/classify_test.go`, the existing tests call `Classify(...)` expecting a bare `model.Class`. Update them to read `.Class`:

In `TestFirstHitWins`, change:
```go
	got := Classify(stack(iamChange), rules, def)
	if got.Name != "iam" {
```
to:
```go
	got := Classify(stack(iamChange), rules, def)
	if got.Class.Name != "iam" {
```

In `TestActionsAndMinCount`, change both assertions:
```go
	if Classify(oneDelete, rules, def).Name != "safe" {
```
→
```go
	if Classify(oneDelete, rules, def).Class.Name != "safe" {
```
and
```go
	if Classify(twoDeletes, rules, def).Name != "destructive" {
```
→
```go
	if Classify(twoDeletes, rules, def).Class.Name != "destructive" {
```

In `TestDefaultWhenNoMatch`, change:
```go
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}}), rules, def)
	if got != def {
		t.Fatalf("no match should yield default %+v, got %+v", def, got)
	}
```
to:
```go
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}}), rules, def)
	if got.Class != def {
		t.Fatalf("no match should yield default %+v, got %+v", def, got.Class)
	}
```

- [ ] **Step 2: Add the new extraction tests**

Append to `internal/classify/classify_test.go`:

```go
func TestEmitAttributesFromMatchedChangesOnly(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	def := model.Class{Name: "safe"}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"update"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	got := Classify(s, rules, def)
	if got.Class.Name != "iam" {
		t.Fatalf("class = %q, want iam", got.Class.Name)
	}
	if len(got.Attributes["project"]) != 1 || got.Attributes["project"][0] != "p1" {
		t.Fatalf("project = %v, want [p1] (bucket's p2 must NOT appear)", got.Attributes["project"])
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
	got := Classify(s, rules, model.Class{Name: "safe"})
	want := []string{"p1", "p2"}
	if len(got.Attributes["project"]) != 2 || got.Attributes["project"][0] != want[0] || got.Attributes["project"][1] != want[1] {
		t.Fatalf("project = %v, want %v", got.Attributes["project"], want)
	}
}

func TestEmitAttributesNilWhenNoneConfigured(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	got := Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules, model.Class{Name: "safe"})
	if got.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no emit_attributes", got.Attributes)
	}
}

func TestEmitAttributesNilWhenNoValuesFound(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	// org-level binding: matches iam, but has no "project" attribute.
	got := Classify(
		stack(plan.RawChange{Type: "google_organization_iam_binding", Actions: []string{"create"}, Raw: map[string]any{"role": "roles/x"}}),
		rules, model.Class{Name: "safe"})
	if got.Class.Name != "iam" {
		t.Fatalf("class = %q, want iam", got.Class.Name)
	}
	if got.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no project values", got.Attributes)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/classify/ -v`
Expected: FAIL — `got.Class` / `Rule.EmitAttributes` / `got.Attributes` undefined.

- [ ] **Step 4: Add `EmitAttributes`, `Result`, and change `Classify`**

In `internal/classify/classify.go`, update the import block to:

```go
import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
)
```

Add `EmitAttributes` to `Rule` (after `MinCount int`):

```go
	EmitAttributes []string // attribute names to extract from matched changes
```

Add the `Result` type above `Classify`:

```go
// Result is the outcome of classifying a stack: the chosen class, plus — for
// the firing rule's EmitAttributes — the sorted-unique non-null values found
// across the changes that rule matched. Attributes is nil when nothing emits.
type Result struct {
	Class      model.Class
	Attributes map[string][]string
}
```

Replace `Classify` (and add `extract`/`scalarString`) with:

```go
// Classify returns the Result for the first rule that matches enough changes,
// or def (with no attributes) when none match.
func Classify(s plan.RawStack, rules []Rule, def model.Class) Result {
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
			return Result{
				Class:      model.Class{Name: r.Name, Icon: r.Icon},
				Attributes: extract(matched, r.EmitAttributes),
			}
		}
	}
	return Result{Class: def}
}

// extract collects sorted-unique non-null scalar values of each requested
// attribute across the matched changes. Returns nil when names is empty or no
// values were found, so the sidecar omits the field.
func extract(matched []plan.RawChange, names []string) map[string][]string {
	if len(names) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, name := range names {
		seen := map[string]struct{}{}
		var vals []string
		for _, c := range matched {
			v, ok := c.Raw[name]
			if !ok || v == nil {
				continue
			}
			str := scalarString(v)
			if _, dup := seen[str]; dup {
				continue
			}
			seen[str] = struct{}{}
			vals = append(vals, str)
		}
		if len(vals) > 0 {
			sort.Strings(vals)
			out[name] = vals
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scalarString stringifies a JSON scalar for the sidecar.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/classify/ -v`
Expected: PASS (updated existing tests + 4 new tests). `cmd` and `config` won't build yet — that's fixed in later tasks.

- [ ] **Step 6: Commit**

```bash
git add internal/classify/classify.go internal/classify/classify_test.go
git commit -m "classify: Result type + emit_attributes extraction from matched changes"
```

---

## Task 3: Propagate `emit_attributes` through `presets`

**Files:**
- Modify: `internal/presets/presets.go` (func `Get` ~line 24)
- Test: `internal/presets/presets_test.go`

- [ ] **Step 1: Update existing tests + add a propagation test**

In `internal/presets/presets_test.go`, update the three `Get` calls to the new 3-arg signature:

- `Get("iam", "")` → `Get("iam", "", nil)`
- `Get("iam", "⚠️")` → `Get("iam", "⚠️", nil)`
- `Get("nope", "")` → `Get("nope", "", nil)`

Then append:

```go
func TestEmitAttributesPropagated(t *testing.T) {
	r, _ := Get("iam", "", []string{"project"})
	if len(r.EmitAttributes) != 1 || r.EmitAttributes[0] != "project" {
		t.Fatalf("EmitAttributes = %v, want [project]", r.EmitAttributes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/presets/ -v`
Expected: FAIL — `Get` takes 2 args / `EmitAttributes` not set.

- [ ] **Step 3: Update `Get`**

In `internal/presets/presets.go`, replace `Get` with:

```go
// Get returns the rule bundle for name. iconOverride replaces the preset's
// default glyph when non-empty; emitAttributes is carried onto the rule. ok is
// false for unknown names.
func Get(name, iconOverride string, emitAttributes []string) (classify.Rule, bool) {
	switch name {
	case "iam":
		r := classify.Rule{Name: "iam", Icon: "🔐", TypePattern: iamPattern, MinCount: 1, EmitAttributes: emitAttributes}
		if iconOverride != "" {
			r.Icon = iconOverride
		}
		return r, true
	default:
		return classify.Rule{}, false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/presets/ -v`
Expected: PASS. (`config` still won't build — fixed next task.)

- [ ] **Step 5: Commit**

```bash
git add internal/presets/presets.go internal/presets/presets_test.go
git commit -m "presets: carry emit_attributes onto the rule"
```

---

## Task 4: Parse `emit_attributes` in `config`

**Files:**
- Modify: `internal/config/config.go` (`ruleBody` ~line 106; `presetBody` ~line 113; `decodeClassification` preset/rule cases ~line 137-166)
- Test: `internal/config/config_test.go`; Create: `internal/config/testdata/emit.hcl`

- [ ] **Step 1: Create the test fixture**

Create `internal/config/testdata/emit.hcl`:

```hcl
classification {
  default {
    name = "safe"
    icon = "✅"
  }

  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }

  rule "destructive" {
    icon            = "💣"
    actions         = ["delete"]
    min_count       = 1
    emit_attributes = ["name", "id"]
  }
}
```

- [ ] **Step 2: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoadEmitAttributes(t *testing.T) {
	c, err := Load("testdata/emit.hcl")
	if err != nil {
		t.Fatal(err)
	}
	rules := c.Classification.Rules
	if len(rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rules))
	}
	// preset "iam" carries emit_attributes
	if len(rules[0].EmitAttributes) != 1 || rules[0].EmitAttributes[0] != "project" {
		t.Fatalf("iam EmitAttributes = %v, want [project]", rules[0].EmitAttributes)
	}
	// custom rule "destructive" carries emit_attributes
	if len(rules[1].EmitAttributes) != 2 || rules[1].EmitAttributes[0] != "name" || rules[1].EmitAttributes[1] != "id" {
		t.Fatalf("destructive EmitAttributes = %v, want [name id]", rules[1].EmitAttributes)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadEmitAttributes -v`
Expected: FAIL — `EmitAttributes` always empty (HCL field not decoded yet). (Package should still compile once Task 3's `Get` signature is matched in Step 4 below; if you run before that, expect a build error on the `presets.Get` call — that is fixed in this task's Step 4.)

- [ ] **Step 4: Add the HCL fields and pass-through**

In `internal/config/config.go`:

Add to `ruleBody` (after `MinCount int`):
```go
	EmitAttributes []string `hcl:"emit_attributes,optional"`
```

Add to `presetBody`:
```go
type presetBody struct {
	Icon           string   `hcl:"icon,optional"`
	EmitAttributes []string `hcl:"emit_attributes,optional"`
}
```

In `decodeClassification`, the `case "preset"` — update the `presets.Get` call:
```go
			rule, ok := presets.Get(b.Labels[0], pb.Icon, pb.EmitAttributes)
```

In `decodeClassification`, the `case "rule"` — set the field on the constructed rule:
```go
			rule := classify.Rule{Name: b.Labels[0], Icon: rb.Icon, Actions: rb.Actions, MinCount: rb.MinCount, EmitAttributes: rb.EmitAttributes}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (new test + all existing config tests).

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go internal/config/testdata/emit.hcl
git commit -m "config: parse emit_attributes on preset and rule"
```

---

## Task 5: Wire into the sidecar in `cmd` + end-to-end test

**Files:**
- Modify: `cmd/tfstackplan/main.go` (`classEntry` ~line 256; `Classify` call-site ~line 174-178)
- Test: `cmd/tfstackplan/main_test.go`

- [ ] **Step 1: Write the failing end-to-end test**

Append to `cmd/tfstackplan/main_test.go`:

```go
func TestRunEmitsClassificationAttributes(t *testing.T) {
	dir := t.TempDir()

	// IAM stack: two members across two projects; one safe stack with none.
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
	iamPath := filepath.Join(dir, "iam.json")
	safePath := filepath.Join(dir, "safe.json")
	cfgPath := filepath.Join(dir, "cfg.hcl")
	classOut := filepath.Join(dir, "classes.json")
	for p, c := range map[string]string{iamPath: iamPlan, safePath: safePlan, cfgPath: cfg} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := run(opts{
		stacks:    []string{"platform/nonprod:" + iamPath, "data/warehouse:" + safePath},
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

	// Safe stack: "attributes" key must be absent entirely (omitempty), even
	// though its bucket has a "project" attribute.
	if !strings.Contains(string(data), "platform/nonprod") {
		t.Fatal("sidecar missing iam stack")
	}
	if strings.Contains(string(data), "fh-data") {
		t.Fatal("safe stack must not emit attributes (omitempty); found its project")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/tfstackplan/ -run TestRunEmitsClassificationAttributes -v`
Expected: FAIL — `classEntry` has no `Attributes`, and the `Classify` call-site doesn't compile against the new `Result` (build error). Both fixed next.

- [ ] **Step 3: Add `Attributes` to `classEntry`**

In `cmd/tfstackplan/main.go`, replace the `classEntry` struct (~line 256):

```go
type classEntry struct {
	Class      string              `json:"class"`
	Icon       *string             `json:"icon"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}
```

- [ ] **Step 4: Update the `Classify` call-site**

In `cmd/tfstackplan/main.go`, replace the `if classified { ... }` block (~line 174-178):

```go
		if classified {
			res := classify.Classify(raw, cfg.Classification.Rules, cfg.Classification.Default)
			st.Class = &res.Class
			sidecar[ref.Name] = classEntry{Class: res.Class.Name, Icon: nilable(res.Class.Icon), Attributes: res.Attributes}
		}
```

- [ ] **Step 5: Run the full suite to verify it passes**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 6: Commit**

```bash
git add cmd/tfstackplan/main.go cmd/tfstackplan/main_test.go
git commit -m "cmd: emit classification attributes in sidecar JSON"
```

---

## Task 6: Document `emit_attributes`

**Files:**
- Modify: `README.md` (Classification section — the "Sidecar JSON for CI gating" subsection ~lines 249-266)

- [ ] **Step 1: Add the `emit_attributes` documentation**

In `README.md`, immediately after the existing "Sidecar JSON for CI gating" code/JSON examples (after the paragraph ending "the flag is a no-op without classification."), insert:

````markdown
#### Emitting matched attributes

A `rule` or `preset` can also surface attributes of the changes it matched, so
CI can gate on *which subjects* triggered the class — e.g. the GCP projects with
IAM changes:

```hcl
classification {
  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }
}
```

The sidecar then carries the sorted-unique, non-null values per stack:

```json
{
  "platform/nonprod": {
    "class": "iam", "icon": "🔐",
    "attributes": { "project": ["fh-host-nonprod", "fh-svc-dev"] }
  }
}
```

- Values come from the **matched changes only** (a `safe` stack emits nothing),
  read from each change's `after` (falling back to `before` for deletes).
- **Top-level scalar attributes only**; nested paths are not supported.
- **Sensitive values are never emitted.**
- `attributes` is omitted when the firing rule declares no `emit_attributes` or
  no values were found.
````

- [ ] **Step 2: Verify the build/tests still pass (docs-only, sanity)**

Run: `go test ./...`
Expected: PASS (unchanged).

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document emit_attributes in classification sidecar"
```

---

## Final verification

- [ ] **Run the whole suite + vet**

Run: `go test ./... && go vet ./...`
Expected: all PASS, no vet findings.

- [ ] **Manual smoke test**

```bash
go build -o /tmp/tfstackplan ./cmd/tfstackplan
# Reuse the test fixture shape: an IAM create with a project attribute.
mkdir -p /tmp/smoke && cat > /tmp/smoke/plan.json <<'JSON'
{"format_version":"1.2","resource_changes":[
 {"address":"google_project_iam_member.a","type":"google_project_iam_member","name":"a",
  "change":{"actions":["create"],"after":{"role":"roles/x","project":"fh-host-nonprod"},
   "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}]}
JSON
cat > /tmp/smoke/policy.hcl <<'HCL'
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
/tmp/tfstackplan --stack platform/nonprod:/tmp/smoke/plan.json \
  --config /tmp/smoke/policy.hcl \
  --emit-classification-json /tmp/smoke/classes.json --output /dev/null
cat /tmp/smoke/classes.json
```
Expected: `attributes.project` = `["fh-host-nonprod"]` on `platform/nonprod`.

---

## Self-review notes

- **Spec coverage:** generic `emit_attributes` (Tasks 3/4) ✓; matched-changes-only extraction (Task 2) ✓; after→before / nil-drop / sort-unique (Task 2) ✓; top-level scalar only + sensitive skip (Task 1) ✓; `omitempty` sidecar shape (Task 5) ✓; docs (Task 6) ✓.
- **Type consistency:** `Classify` returns `classify.Result{Class model.Class; Attributes map[string][]string}` — used identically in Task 2 (tests), Task 5 (`res.Class`, `res.Attributes`). `Rule.EmitAttributes []string` consistent across classify/presets/config. `classEntry.Attributes map[string][]string` matches `Result.Attributes`. `presets.Get(name, iconOverride string, emitAttributes []string)` consistent in presets + config call-site.
- **No placeholders:** every code/step is concrete.
- **Out of scope (unchanged):** markdown rendering, nested-attribute extraction, class computation.
