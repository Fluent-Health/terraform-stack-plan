# Fractal per-resource output redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure the per-stack body so creates/deletes show their attributes, updates are aligned and grouped, and large diffs fold behind nested `<details>` — a fractal, drill-down report.

**Architecture:** An attribute (`model.Field`) is rendered one of two ways: as **leaves** (aligned `op path = value` rows — scalars, small structural diffs, sensitive/unknown) or as a foldable **block** (the existing `Variant` ladder — line diffs, base64, large structural diffs). `plan` now extracts attributes for every action. `render` groups by resource: updates emit a header + aligned inline leaves, with block attrs folding into nested `<details>`; creates/deletes fold the whole resource into a `<details>` whose body is the aligned leaves. `fit` keeps degrading block variants largest-first; leaves never degrade. Render aligns *after* `fit` selects variants.

**Tech Stack:** Go 1.23, `terraform-json`, `go-difflib`, `gopkg.in/yaml.v3`. Tests are stdlib `testing`; examples are golden files under `examples/` driven by `cmd/tfstackplan` TestExamples.

---

## File Structure

- `internal/model/model.go` — add `Leaf`, `LeafOp`, and `Field` (replaces the bare `AttrDiff` role on `Change.Attrs`); keep `Variant`/`Level` for blocks.
- `internal/plan/plan.go` — extract `changedAttrs` for create (`after`) and delete (`before`); add `Action`-aware attribute extraction.
- `internal/differ/differ.go` — produce a `model.Field` with either `Leaves` or a block `Variant` ladder; new `op path = value` / `→` leaf format; structural paths prefixed with the attribute name; a leaf-only mode for create/delete.
- `internal/render/render.go` — per-resource grouping, leaf alignment, nested `<details>` for create/delete and large update attrs, fence management.
- `internal/fit/fit.go` — `largestDegradable` already skips attrs at their last variant; verify leaf-only fields (no variants) are skipped.
- `cmd/tfstackplan/genplan_test.go` + `examples_test.go` — add a structured-string (YAML/JSON) fixture; regenerate goldens; retune budgets.
- `README.md` — update "What it looks like".

Define `Leaf`/`Field` in Task 1; every later task references those exact names.

---

## Task 1: Model — Leaf and Field types

**Files:**
- Modify: `internal/model/model.go`
- Test: `internal/model/model_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/model/model_test.go`:

```go
func TestLeafValue(t *testing.T) {
	cases := []struct {
		name string
		leaf Leaf
		want string
	}{
		{"add", Leaf{Op: OpAdd, Path: "team", New: `"platform"`}, `"platform"`},
		{"remove", Leaf{Op: OpRemove, Path: "id", Old: `"x"`}, `"x"`},
		{"change", Leaf{Op: OpChange, Path: "n", Old: "7", New: "30"}, "7 → 30"},
		{"inline override", Leaf{Op: OpChange, Path: "pw", Old: "a", New: "b", Inline: "(sensitive value)"}, "(sensitive value)"},
	}
	for _, c := range cases {
		if got := c.leaf.Value(); got != c.want {
			t.Errorf("%s: Value() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFieldIsBlock(t *testing.T) {
	leafField := Field{Name: "labels", Leaves: []Leaf{{Op: OpAdd, Path: "labels.team", New: `"x"`}}}
	if leafField.IsBlock() {
		t.Errorf("leaf field should not be a block")
	}
	blockField := Field{Name: "data", Variants: []Variant{{Level: LevelLineDiff, Content: "x"}}}
	if !blockField.IsBlock() {
		t.Errorf("variant field should be a block")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/model/ -run 'TestLeafValue|TestFieldIsBlock' -v`
Expected: FAIL — `OpAdd` / `Leaf` / `Field` undefined.

- [ ] **Step 3: Add the types**

In `internal/model/model.go`, add (after the `Class` block):

```go
// LeafOp is the change kind for a single leaf attribute path.
type LeafOp uint8

const (
	OpAdd    LeafOp = iota // added leaf
	OpChange               // changed leaf
	OpRemove               // removed leaf
)

// Sym returns the diff prefix for the op (+, ~, -).
func (o LeafOp) Sym() string {
	switch o {
	case OpAdd:
		return "+"
	case OpRemove:
		return "-"
	default:
		return "~"
	}
}

// Leaf is one aligned `op path = value` row.
type Leaf struct {
	Op     LeafOp
	Path   string // dotted, includes the attribute name (e.g. "labels.team")
	Old    string // rendered scalar; used for change/remove
	New    string // rendered scalar; used for add/change
	Inline string // when set, rendered verbatim instead of Old/New (e.g. "(sensitive value)")
}

// Value returns the right-hand side of the `=`.
func (l Leaf) Value() string {
	if l.Inline != "" {
		return l.Inline
	}
	switch l.Op {
	case OpAdd:
		return l.New
	case OpRemove:
		return l.Old
	default:
		return l.Old + " → " + l.New
	}
}

// Field is one top-level attribute of a resource change. It renders either as
// aligned Leaves (scalars, small structural diffs, sensitive/unknown) or, when
// large, as a foldable block carrying the Variant ladder fit degrades.
type Field struct {
	Name     string
	Leaves   []Leaf    // inline rows; empty when this is a block
	Variants []Variant // block ladder; empty when this is leaves
	Selected int       // chosen variant (block only); fit mutates
}

// IsBlock reports whether this field renders as a foldable block.
func (f Field) IsBlock() bool { return len(f.Variants) > 0 }

// Sel returns the selected block variant (block fields only).
func (f Field) Sel() Variant { return f.Variants[f.Selected] }

// AtLast reports whether the selected block variant is the least-detail one.
func (f Field) AtLast() bool { return f.Selected >= len(f.Variants)-1 }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/model/ -run 'TestLeafValue|TestFieldIsBlock' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/model/model.go internal/model/model_test.go
git commit -m "model: add Leaf and Field types for aligned/foldable attributes"
```

---

## Task 2: Model — switch Change.Attrs to []Field

**Files:**
- Modify: `internal/model/model.go:59-85` (the `AttrDiff`/`Change` block)
- Modify: `internal/model/model_test.go` (if it references `Change.Attrs`)

This is a type rename on the `Change` struct. `AttrDiff` stays (the differ still builds variant ladders into it) but `Change.Attrs` becomes `[]Field`. Downstream packages won't compile until Tasks 3–6; that's expected — this task only changes the model and its own tests.

- [ ] **Step 1: Change the Change struct**

In `internal/model/model.go`, replace the `Change` struct's `Attrs` field:

```go
// Change is one resource change within a stack.
type Change struct {
	Address string
	Type    string
	Action  Action
	Fields  []Field // populated for create/delete/update/replace
}
```

(Rename `Attrs []AttrDiff` → `Fields []Field`. Keep `AttrDiff`/`Variant`/`Level` as-is — the differ uses them to build a `Field`'s `Variants`.)

- [ ] **Step 2: Update model tests that reference the old field**

Search: `grep -rn "\.Attrs" internal/model/`. If any model test builds `Change{Attrs: ...}`, change it to `Fields: []Field{...}`.

- [ ] **Step 3: Verify the model package compiles and tests pass**

Run: `go test ./internal/model/ -v`
Expected: PASS. (Other packages will not build yet — that's fine; do not run `./...` here.)

- [ ] **Step 4: Commit**

```bash
git add internal/model/
git commit -m "model: Change carries []Field instead of []AttrDiff"
```

---

## Task 3: Plan — extract attributes for create and delete

**Files:**
- Modify: `internal/plan/plan.go:68-80` and `changedAttrs` (105-140)
- Test: `internal/plan/plan_test.go`, fixtures in `internal/plan/testdata/`

Today `changedAttrs` runs only for update/replace and diffs before↔after. Add extraction for create (all of `after`) and delete (all of `before`), so `RawChange.Attrs` is populated for every rendered action. `RawAttr` already carries `Before`/`After`/`Sensitive`/`Unknown`.

- [ ] **Step 1: Write the failing test**

Add a fixture `internal/plan/testdata/create.json`:

```json
{
  "format_version": "1.2",
  "resource_changes": [
    {"address":"google_service_account.api","type":"google_service_account","name":"api",
     "change":{"actions":["create"],"before":null,
       "after":{"account_id":"app-api","disabled":false},
       "after_unknown":{"unique_id":true},"before_sensitive":{},"after_sensitive":{}}}
  ]
}
```

Append to `internal/plan/plan_test.go`:

```go
func TestParseCreateExtractsAfterAttrs(t *testing.T) {
	data, err := os.ReadFile("testdata/create.json")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Parse("svc", data)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Counts.Add != 1 {
		t.Fatalf("Add count = %d, want 1", rs.Counts.Add)
	}
	got := map[string]RawAttr{}
	for _, a := range rs.Changes[0].Attrs {
		got[a.Name] = a
	}
	if got["account_id"].After != "app-api" {
		t.Errorf("account_id After = %v, want app-api", got["account_id"].After)
	}
	if !got["unique_id"].Unknown {
		t.Errorf("unique_id should be known-after-apply")
	}
}
```

(`os` is already imported in `plan_test.go`; if not, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plan/ -run TestParseCreateExtractsAfterAttrs -v`
Expected: FAIL — `rs.Changes[0].Attrs` empty for a create.

- [ ] **Step 3: Implement action-aware extraction**

In `internal/plan/plan.go`, replace the attribute-population block in `Parse` (currently the `if bucket == model.ActionChange || bucket == model.ActionReplace` guard):

```go
		switch bucket {
		case model.ActionChange, model.ActionReplace:
			ch.Attrs = changedAttrs(rc.Change)
		case model.ActionAdd:
			ch.Attrs = sideAttrs(rc.Change, true)
		case model.ActionDestroy:
			ch.Attrs = sideAttrs(rc.Change, false)
		}
```

Add `sideAttrs` near `changedAttrs`:

```go
// sideAttrs lists every leaf attribute of a create (after=true) or delete
// (after=false), carrying sensitive / known-after-apply markers.
func sideAttrs(c *tfjson.Change, after bool) []RawAttr {
	src, _ := c.After.(map[string]any)
	sens, _ := c.AfterSensitive.(map[string]any)
	unknown, _ := c.AfterUnknown.(map[string]any)
	if !after {
		src, _ = c.Before.(map[string]any)
		sens, _ = c.BeforeSensitive.(map[string]any)
		unknown = nil // deletes have no after_unknown
	}

	keys := map[string]struct{}{}
	for k := range src {
		keys[k] = struct{}{}
	}
	for k := range unknown {
		keys[k] = struct{}{}
	}

	var attrs []RawAttr
	for k := range keys {
		ra := RawAttr{Name: k, Sensitive: truthy(sens[k]), Unknown: truthy(unknown[k])}
		if after {
			ra.After = src[k]
		} else {
			ra.Before = src[k]
		}
		attrs = append(attrs, ra)
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	return attrs
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plan/ -run TestParseCreateExtractsAfterAttrs -v`
Expected: PASS. Also run `go test ./internal/plan/ -v` — existing update/delete/mixed tests must still pass (deletes now carry attrs; if a test asserted empty attrs for delete, update it to expect the extracted set).

- [ ] **Step 5: Commit**

```bash
git add internal/plan/
git commit -m "plan: extract attributes for create (after) and delete (before)"
```

---

## Task 4: Differ — leaf output for scalars, sensitive, unknown

**Files:**
- Modify: `internal/differ/differ.go` — `Diff` now returns `model.Field`; add leaf builders.
- Test: `internal/differ/diff_test.go`

The differ's return type changes from `model.AttrDiff` to `model.Field`. Scalars/sensitive/unknown become a single-leaf field; structured/large values become a block field (`Variants` set). This task does the leaf cases and the type switch; Task 5 does structural-as-leaves; the block path keeps the existing ladder by assigning it to `Field.Variants`.

- [ ] **Step 1: Write the failing test**

Replace `TestScalarInline`, `TestSensitiveInline`, `TestUnknownInline` in `diff_test.go` with leaf-based assertions, and update the `levels` helper to read `ad.Variants` from a `model.Field`:

```go
func levels(f model.Field) []model.Level {
	var out []model.Level
	for _, v := range f.Variants {
		out = append(out, v.Level)
	}
	return out
}

func TestScalarLeaf(t *testing.T) {
	f := Diff(Input{Attr: "role", Before: "roles/viewer", After: "roles/editor"})
	if f.IsBlock() || len(f.Leaves) != 1 {
		t.Fatalf("scalar should be one leaf, got block=%v leaves=%d", f.IsBlock(), len(f.Leaves))
	}
	l := f.Leaves[0]
	if l.Op != model.OpChange || l.Path != "role" || l.Value() != `"roles/viewer" → "roles/editor"` {
		t.Fatalf("unexpected leaf: %+v value=%q", l, l.Value())
	}
}

func TestSensitiveLeaf(t *testing.T) {
	f := Diff(Input{Attr: "password", Before: "a", After: "b", Sensitive: true})
	if len(f.Leaves) != 1 || f.Leaves[0].Inline != "(sensitive value)" {
		t.Fatalf("sensitive should be one leaf with inline marker, got %+v", f.Leaves)
	}
}

func TestUnknownLeaf(t *testing.T) {
	f := Diff(Input{Attr: "id", Unknown: true})
	if len(f.Leaves) != 1 || f.Leaves[0].Inline != "(known after apply)" {
		t.Fatalf("unknown should be one leaf with inline marker, got %+v", f.Leaves)
	}
}

func TestCreateLeaf(t *testing.T) {
	f := Diff(Input{Attr: "account_id", After: "app-api"})
	if len(f.Leaves) != 1 || f.Leaves[0].Op != model.OpAdd || f.Leaves[0].Value() != `"app-api"` {
		t.Fatalf("create-only value should be an add leaf, got %+v", f.Leaves)
	}
}

func TestDeleteLeaf(t *testing.T) {
	f := Diff(Input{Attr: "name", Before: "legacy"})
	if len(f.Leaves) != 1 || f.Leaves[0].Op != model.OpRemove || f.Leaves[0].Value() != `"legacy"` {
		t.Fatalf("delete-only value should be a remove leaf, got %+v", f.Leaves)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/differ/ -run 'Leaf$' -v`
Expected: FAIL — `Diff` returns `AttrDiff`, no `Leaves`.

- [ ] **Step 3: Change `Diff`'s signature and add leaf builders**

In `differ.go`: change `func Diff(in Input) model.AttrDiff` → `func Diff(in Input) model.Field`. Replace the always-inline and scalar branches to build leaves; add a helper `scalarLeaf`. Keep `inline`/`single`/`ladderFrom` for blocks but have them return/compose into `model.Field`.

Replace the top of `Diff` (the `Unknown`/`Sensitive` and scalar branches):

```go
func Diff(in Input) model.Field {
	// Always-inline cases → single leaf.
	switch {
	case in.Unknown:
		return leafField(in, model.OpChange, "(known after apply)")
	case in.Sensitive:
		return leafField(in, model.OpChange, "(sensitive value)")
	}

	bs, bIsStr := in.Before.(string)
	as, aIsStr := in.After.(string)

	// Create-only / delete-only scalar (one side nil).
	if in.Before == nil && in.After != nil && !aIsStr && !isStructured(in.Before, in.After) {
		return scalarLeaf(in.Attr, model.OpAdd, "", scalar(in.After))
	}
	if in.After == nil && in.Before != nil && !bIsStr && !isStructured(in.Before, in.After) {
		return scalarLeaf(in.Attr, model.OpRemove, scalar(in.Before), "")
	}

	// Forced "hide"/"summary" short-circuit (block).
	switch in.ForceDiffer {
	case "hide":
		return blockField(single(in.Attr, model.LevelHidden, ""))
	case "summary":
		return blockField(ladderFrom(in.Attr, model.LevelSummary, in))
	}

	// Native structured (maps/lists) → structural (Task 5 decides leaves vs block).
	if !bIsStr && !aIsStr && isStructured(in.Before, in.After) {
		return structural(in)
	}

	// Both scalar (non-string) → inline leaf.
	if !isStructured(in.Before, in.After) && !bIsStr && !aIsStr {
		return scalarLeaf(in.Attr, model.OpChange, scalar(in.Before), scalar(in.After))
	}

	// String values: detect → structural leaves (Task 5) or block.
	kind := in.ForceDiffer
	if kind == "" || kind == "auto" {
		if in.NoDetect {
			kind = "line"
		} else {
			switch detect(firstNonEmpty(bs, as)) {
			case typeJSON, typeYAML:
				return structural(in)
			case typeBase64:
				return blockField(ladderFrom(in.Attr, model.LevelSummary, in))
			default:
				kind = "line"
			}
		}
	}
	switch kind {
	case "structural", "json", "yaml":
		return structural(in)
	default: // line
		if !strings.Contains(bs, "\n") && !strings.Contains(as, "\n") && len(bs) < 60 && len(as) < 60 {
			return scalarLeaf(in.Attr, model.OpChange, fmt.Sprintf("%q", bs), fmt.Sprintf("%q", as))
		}
		return blockField(ladderFrom(in.Attr, model.LevelLineDiff, in))
	}
}

// leafField builds a one-leaf field whose value is rendered verbatim (markers).
func leafField(in Input, op model.LeafOp, marker string) model.Field {
	return model.Field{Name: in.Attr, Leaves: []model.Leaf{{Op: op, Path: in.Attr, Inline: marker}}}
}

// scalarLeaf builds a one-leaf field from already-rendered scalar strings.
func scalarLeaf(attr string, op model.LeafOp, old, new string) model.Field {
	return model.Field{Name: attr, Leaves: []model.Leaf{{Op: op, Path: attr, Old: old, New: new}}}
}

// blockField wraps a variant ladder (built by the existing helpers) as a Field.
func blockField(ad model.AttrDiff) model.Field {
	return model.Field{Name: ad.Name, Variants: ad.Variants}
}
```

Add a temporary `structural` that delegates to the block ladder so this task compiles (Task 5 replaces it):

```go
// structural renders a map/JSON/YAML attribute. Task 5 turns small diffs into
// leaves; for now it always produces the block ladder.
func structural(in Input) model.Field {
	return blockField(ladderFrom(in.Attr, model.LevelStructural, in))
}
```

Note: `ladderFrom`/`inline`/`single` still return `model.AttrDiff`; only `Diff` returns `model.Field`. Leave them returning `AttrDiff` and wrap with `blockField`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/differ/ -run 'Leaf$' -v`
Expected: PASS. Then `go test ./internal/differ/ -v`; update the surviving ladder tests (`TestStructuredYAMLLadder`, `TestBase64Ladder`, `TestForcedDifferLine`, `TestMaxAttributeLinesCeiling`, `TestNativeMapStructuralSummaryHasCounts`, `TestNoDetectForcesLine`, `TestForcedSummary`) to read `f.Variants` via the new `levels(model.Field)` helper — assertions are otherwise unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/differ/
git commit -m "differ: Diff returns model.Field; scalar/sensitive/unknown/create/delete render as leaves"
```

---

## Task 5: Differ — small structural diffs as named, aligned leaves

**Files:**
- Modify: `internal/differ/differ.go` — `structural`, `structuralDiff`/`flatten` usage.
- Test: `internal/differ/diff_test.go`

Restore the attribute name in nested paths and emit per-leaf ops, but only inline when the diff is small; otherwise keep the block ladder.

- [ ] **Step 1: Write the failing test**

```go
func TestStructuralSmallBecomesLeaves(t *testing.T) {
	before := map[string]any{"env": "nonprod"}
	after := map[string]any{"env": "nonprod", "team": "platform"}
	f := Diff(Input{Attr: "labels", Before: before, After: after})
	if f.IsBlock() {
		t.Fatalf("small structural diff should be leaves, not a block")
	}
	if len(f.Leaves) != 1 {
		t.Fatalf("want 1 changed leaf, got %d (%+v)", len(f.Leaves), f.Leaves)
	}
	l := f.Leaves[0]
	if l.Op != model.OpAdd || l.Path != "labels.team" || l.Value() != `"platform"` {
		t.Fatalf("leaf should be `+ labels.team = \"platform\"`, got %+v value=%q", l, l.Value())
	}
}

func TestStructuralLargeStaysBlock(t *testing.T) {
	before := map[string]any{}
	after := map[string]any{}
	for i := 0; i < 40; i++ {
		before[fmt.Sprintf("k%02d", i)] = "old"
		after[fmt.Sprintf("k%02d", i)] = "new"
	}
	f := Diff(Input{Attr: "settings", Before: before, After: after})
	if !f.IsBlock() {
		t.Fatalf("40-leaf structural diff should stay a foldable block")
	}
}
```

(`fmt` is imported in the differ test? add it if needed.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/differ/ -run 'TestStructural(Small|Large)' -v`
Expected: FAIL — `structural` always returns a block; paths lack the attribute name.

- [ ] **Step 3: Implement leaf extraction with a fold threshold**

Add the fold threshold constant and a structural leaf builder. Reuse `flatten` but seed the prefix with the attribute name:

```go
// foldThreshold is the leaf/line count at or above which an attribute folds
// into a block (its own <details>) instead of rendering inline.
const foldThreshold = 10

// structural emits aligned leaves for a map/JSON/YAML attribute when the change
// is small; otherwise it keeps the block ladder (which fit can degrade).
func structural(in Input) model.Field {
	bv := parseStructured(in.Before, firstStr(in.Before))
	av := parseStructured(in.After, firstStr(in.After))
	leaves := structuralLeaves(in.Attr, bv, av)
	if len(leaves) > 0 && len(leaves) < foldThreshold {
		return model.Field{Name: in.Attr, Leaves: leaves}
	}
	return blockField(ladderFrom(in.Attr, model.LevelStructural, in))
}

// firstStr returns v as a string if it is one, else "".
func firstStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// structuralLeaves diffs two structured values into leaves, with paths prefixed
// by the attribute name.
func structuralLeaves(attr string, before, after any) []model.Leaf {
	bm, am := map[string]string{}, map[string]string{}
	flatten(attr, before, bm)
	flatten(attr, after, am)

	keys := map[string]struct{}{}
	for k := range bm {
		keys[k] = struct{}{}
	}
	for k := range am {
		keys[k] = struct{}{}
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var leaves []model.Leaf
	for _, k := range sorted {
		bvv, bok := bm[k]
		avv, aok := am[k]
		switch {
		case bok && aok && bvv != avv:
			leaves = append(leaves, model.Leaf{Op: model.OpChange, Path: k, Old: unquote(bvv), New: unquote(avv)})
		case bok && !aok:
			leaves = append(leaves, model.Leaf{Op: model.OpRemove, Path: k, Old: unquote(bvv)})
		case !bok && aok:
			leaves = append(leaves, model.Leaf{Op: model.OpAdd, Path: k, New: unquote(avv)})
		}
	}
	return leaves
}
```

Note: `flatten(attr, ...)` uses the attribute name as the root prefix, so a leaf under `labels` becomes `labels.team`. For a top-level structured value passed with prefix `attr`, scalars at the root collapse to just `attr` (matches a whole-value replacement). Keep `structuralDiff` for the block path (it still feeds the rich variant via `ladderFrom`); it is unaffected.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/differ/ -run 'TestStructural(Small|Large)' -v`
Expected: PASS. Run `go test ./internal/differ/ -v`; the native-map test (`TestNativeMapStructuralSummaryHasCounts`) used a 2-key map that is now leaves — change it to a ≥`foldThreshold`-key map so it still exercises the block ladder, or rename it to assert leaves.

- [ ] **Step 5: Commit**

```bash
git add internal/differ/
git commit -m "differ: small structural diffs render as named aligned leaves; large ones fold"
```

---

## Task 6: Render — leaf alignment and the per-resource update view

**Files:**
- Modify: `internal/render/render.go` — `renderChange` and helpers.
- Test: `internal/render/render_test.go`

Update `sampleReport()` and assertions to the `Field` model, then implement aligned leaf rendering for update/replace resources.

- [ ] **Step 1: Write the failing test**

Update `sampleReport()` in `render_test.go` so the changed stack uses `Fields`:

```go
Changes: []model.Change{{
	Address: "google_storage_bucket.tfstate",
	Type:    "google_storage_bucket",
	Action:  model.ActionChange,
	Fields: []model.Field{
		{Name: "labels", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "labels.team", New: `"platform"`}}},
		{Name: "retention_days", Leaves: []model.Leaf{{Op: model.OpChange, Path: "retention_days", Old: "7", New: "30"}}},
	},
}},
```

Add:

```go
func TestRenderUpdateLeavesAligned(t *testing.T) {
	out := Render(sampleReport())
	// Both leaf keys pad to the longest ("retention_days" = 14 chars), so the
	// `=` signs line up.
	if !strings.Contains(out, "+ labels.team    = \"platform\"") {
		t.Fatalf("labels.team not aligned:\n%s", out)
	}
	if !strings.Contains(out, "~ retention_days = 7 → 30") {
		t.Fatalf("retention_days line wrong:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestRenderUpdateLeavesAligned -v`
Expected: FAIL — `renderChange` still reads `c.Attrs` and old variant content.

- [ ] **Step 3: Implement aligned update rendering**

In `render.go`, replace `renderChange` to use `Fields` and add `alignLeaves`. For this task handle update/replace inline leaves and the create/delete one-liner; block folding comes in Task 7.

```go
func renderChange(b *strings.Builder, c model.Change) {
	switch c.Action {
	case model.ActionAdd, model.ActionDestroy:
		// Folded by the caller in Task 7; for now emit the one-line summary.
		sym := "+"
		if c.Action == model.ActionDestroy {
			sym = "-"
		}
		fmt.Fprintf(b, "%s %s\n", sym, c.Address)
		return
	}
	verb := ""
	if c.Action == model.ActionReplace {
		verb = " · replace"
	}
	fmt.Fprintf(b, "# %s%s\n", c.Address, verb)

	var leaves []model.Leaf
	for _, f := range c.Fields {
		if !f.IsBlock() {
			leaves = append(leaves, f.Leaves...)
		}
	}
	for _, line := range alignLeaves(leaves) {
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// alignLeaves renders leaves as `op path = value`, padding paths so the `=`
// signs align.
func alignLeaves(leaves []model.Leaf) []string {
	w := 0
	for _, l := range leaves {
		if len(l.Path) > w {
			w = len(l.Path)
		}
	}
	out := make([]string, 0, len(leaves))
	for _, l := range leaves {
		pad := strings.Repeat(" ", w-len(l.Path))
		out = append(out, fmt.Sprintf("%s %s%s = %s", l.Op.Sym(), l.Path, pad, l.Value()))
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/render/ -run TestRenderUpdateLeavesAligned -v`
Expected: PASS. Run `go test ./internal/render/ -v`; fix `TestRenderClassColumnAndDetails` etc. to the new content if they assert old attribute strings.

- [ ] **Step 5: Commit**

```bash
git add internal/render/
git commit -m "render: aligned per-resource leaf rendering for updates"
```

---

## Task 7: Render — fold create/delete and large attrs into nested <details>

**Files:**
- Modify: `internal/render/render.go` — `renderDetails`, `renderChange`.
- Test: `internal/render/render_test.go`

A stack's body becomes a sequence: each update/replace resource emits a ` ```diff ` fence (header + inline leaves); each create/delete resource and each block field emits a nested `<details>` containing its own fence. Fences and nested `<details>` must not overlap.

- [ ] **Step 1: Write the failing test**

```go
func TestRenderCreateFolds(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Changes = []model.Change{{
		Address: "google_service_account.api",
		Type:    "google_service_account",
		Action:  model.ActionAdd,
		Fields: []model.Field{
			{Name: "account_id", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "account_id", New: `"app-api"`}}},
			{Name: "disabled", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "disabled", New: "false"}}},
		},
	}}
	r.Stacks[0].Counts = model.Counts{Add: 1}
	out := Render(r)
	if !strings.Contains(out, "<details><summary>+ google_service_account.api · 2 attrs</summary>") {
		t.Fatalf("create should fold into a nested details:\n%s", out)
	}
	if !strings.Contains(out, "+ account_id = \"app-api\"") {
		t.Fatalf("create body should list attributes:\n%s", out)
	}
}

func TestRenderLargeAttrFolds(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Changes = []model.Change{{
		Address: "kubernetes_config_map.app",
		Type:    "kubernetes_config_map",
		Action:  model.ActionChange,
		Fields: []model.Field{{
			Name: "data",
			Variants: []model.Variant{
				{Level: model.LevelLineDiff, Content: "- old\n+ new", Bytes: 9},
				{Level: model.LevelSummary, Content: "  ~ data · text · 2 lines · 2 changed (hidden to fit size limit)"},
				{Level: model.LevelHidden, Content: ""},
			},
		}},
	}}
	r.Stacks[0].Counts = model.Counts{Change: 1}
	out := Render(r)
	if !strings.Contains(out, "<details><summary>~ data") {
		t.Fatalf("large attr should fold into a nested details:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run 'TestRender(CreateFolds|LargeAttrFolds)' -v`
Expected: FAIL — no nested details emitted.

- [ ] **Step 3: Implement folding and fence management**

Rewrite `renderDetails` to drive per-resource segments, and split `renderChange` responsibilities. The stack `<details>` no longer wraps one fence; instead each resource emits its own segment(s):

```go
func renderDetails(b *strings.Builder, r model.Report) {
	for _, s := range r.Stacks {
		if !s.Counts.AnyChange() {
			continue
		}
		summary := s.Name
		if s.Class != nil {
			summary += " · " + s.Class.Label()
		}
		summary += " · " + changeWord(s.Counts)
		open := ""
		if r.DetailsOpen {
			open = " open"
		}
		fmt.Fprintf(b, "\n<details%s><summary>%s</summary>\n", open, summary)
		for _, c := range s.Changes {
			renderResource(b, c)
		}
		b.WriteString("</details>\n")
	}
}

// renderResource emits one resource: a folded <details> for create/delete, or
// (for update/replace) an inline diff fence plus a folded <details> per block
// field.
func renderResource(b *strings.Builder, c model.Change) {
	switch c.Action {
	case model.ActionAdd, model.ActionDestroy:
		op := model.OpAdd
		if c.Action == model.ActionDestroy {
			op = model.OpRemove
		}
		var leaves []model.Leaf
		for _, f := range c.Fields {
			leaves = append(leaves, f.Leaves...)
		}
		fmt.Fprintf(b, "\n<details><summary>%s %s · %d attrs</summary>\n\n```diff\n", op.Sym(), c.Address, len(leaves))
		for _, line := range alignLeaves(leaves) {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("```\n\n</details>\n")
		return
	}

	// update / replace: inline leaves in a fence, then block fields fold.
	verb := ""
	if c.Action == model.ActionReplace {
		verb = " · replace"
	}
	var inline []model.Leaf
	var blocks []model.Field
	for _, f := range c.Fields {
		if f.IsBlock() {
			blocks = append(blocks, f)
		} else {
			inline = append(inline, f.Leaves...)
		}
	}
	b.WriteString("\n```diff\n")
	fmt.Fprintf(b, "# %s%s\n", c.Address, verb)
	for _, line := range alignLeaves(inline) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	for _, f := range blocks {
		v := f.Sel()
		if v.Level == model.LevelHidden || v.Content == "" {
			continue
		}
		lines := lineCountOf(v.Content)
		fmt.Fprintf(b, "\n<details><summary>~ %s · %d lines</summary>\n\n```diff\n%s\n```\n\n</details>\n", f.Name, lines, strings.TrimRight(v.Content, "\n"))
	}
}

func lineCountOf(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}
```

Delete the old single-fence `renderDetails`/`renderChange` body they replace. Keep `renderMinimal`, `renderTable`, `changeWord`, `itoa`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/render/ -run 'TestRender(CreateFolds|LargeAttrFolds)' -v`
Expected: PASS. Run `go test ./internal/render/ -v` and reconcile the remaining assertions (e.g. `TestRenderDetailsOpen`, `TestRenderSummaryOnlyMode`) with the new segment layout.

- [ ] **Step 5: Commit**

```bash
git add internal/render/
git commit -m "render: fold create/delete and large attrs into nested details"
```

---

## Task 8: Fit — verify degradation over Field blocks

**Files:**
- Modify: `internal/fit/fit.go:61-80` — `largestDegradable` iterates `Fields` not `Attrs`.
- Test: `internal/fit/fit_test.go`

`fit` degrades block fields; leaf-only fields have no variants and must be skipped.

- [ ] **Step 1: Update largestDegradable to the Field model**

```go
func largestDegradable(r *model.Report) *model.Field {
	var best *model.Field
	bestBytes := -1
	for si := range r.Stacks {
		for ci := range r.Stacks[si].Changes {
			fields := r.Stacks[si].Changes[ci].Fields
			for fi := range fields {
				f := &fields[fi]
				if !f.IsBlock() || f.AtLast() {
					continue
				}
				if bb := f.Sel().Bytes; bb > bestBytes {
					best = f
					bestBytes = bb
				}
			}
		}
	}
	return best
}
```

And in `Fit`, `a.Selected++` becomes `f.Selected++` (rename the local).

- [ ] **Step 2: Update/verify fit tests**

`fit_test.go` builds synthetic models. Update any `Change{Attrs: []model.AttrDiff{...}}` to `Change{Fields: []model.Field{...}}` with block variants. Add:

```go
func TestFitSkipsLeafFields(t *testing.T) {
	r := &model.Report{Stacks: []model.Stack{{
		Name: "s", Counts: model.Counts{Change: 1},
		Changes: []model.Change{{Address: "a", Action: model.ActionChange,
			Fields: []model.Field{{Name: "x", Leaves: []model.Leaf{{Op: model.OpChange, Path: "x", Old: "1", New: "2"}}}}}},
	}}}
	// A leaf-only report must never panic and always "fits" once small enough.
	_ = Fit(r, 100000)
}
```

- [ ] **Step 3: Run fit tests**

Run: `go test ./internal/fit/ -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/fit/
git commit -m "fit: degrade Field blocks; skip leaf-only fields"
```

---

## Task 9: Wire the gather loop and confirm the binary builds

**Files:**
- Modify: `cmd/tfstackplan/main.go:149-168` — the per-change gather loop builds `[]model.Field`.

- [ ] **Step 1: Update the gather loop**

In `run()`, replace the inner loop that builds `model.Change`:

```go
		for _, rc := range raw.Changes {
			ch := model.Change{Address: rc.Address, Type: rc.Type, Action: rc.Action}
			for _, ra := range rc.Attrs {
				kind := cfg.Diff.Resolve(rc.Type, ra.Name)
				f := differ.Diff(differ.Input{
					ResourceType: rc.Type,
					Attr:         ra.Name,
					Before:       ra.Before,
					After:        ra.After,
					Sensitive:    ra.Sensitive,
					Unknown:      ra.Unknown,
					ForceDiffer:  kind,
					MaxLines:     cfg.Diff.MaxAttributeLines,
					NoDetect:     !cfg.Diff.Detect,
				})
				ch.Fields = append(ch.Fields, f)
			}
			st.Changes = append(st.Changes, ch)
		}
```

- [ ] **Step 2: Build and run the whole suite**

Run: `go build ./... && go test ./internal/... -v`
Expected: build OK; all internal packages PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/tfstackplan/main.go
git commit -m "cmd: gather builds []model.Field"
```

---

## Task 10: Examples — structured-string fixture, regenerate goldens, retune, README

**Files:**
- Modify: `cmd/tfstackplan/genplan_test.go` — add a structured-string generator.
- Modify: `cmd/tfstackplan/examples_test.go` — add the fixture to a stack; retune budgets.
- Regenerate: `examples/*.md`.
- Modify: `README.md` — "What it looks like".

- [ ] **Step 1: Add a structured-string generator**

In `genplan_test.go`:

```go
// yamlUpdate changes a few keys of a YAML string attribute — the structural
// (changed-paths-only) case. n controls how many keys change (small → inline
// leaves, large → folded block).
func yamlUpdate(addr string, changed int) change {
	var before, after strings.Builder
	before.WriteString("spec:\n")
	after.WriteString("spec:\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&before, "  key_%02d: old\n", i)
		v := "old"
		if i < changed {
			v = "new"
		}
		fmt.Fprintf(&after, "  key_%02d: %s\n", i, v)
	}
	return update(addr, "kubernetes_manifest",
		map[string]any{"manifest": before.String()},
		map[string]any{"manifest": after.String()})
}
```

- [ ] **Step 2: Add it to the shared stacks**

In `exampleStacks`, add to the `observability/grafana` stack (or a new stack):

```go
yamlUpdate("kubernetes_manifest.ingress", 2),   // few keys → inline structural leaves
yamlUpdate("kubernetes_manifest.configmap", 11), // many keys → folded block
```

- [ ] **Step 3: Regenerate goldens and check invariants**

Run: `go test ./cmd/tfstackplan -run TestExamples -update`
Expected: the four files regenerate; the four invariant asserts pass. If a budget no longer lands its mode (creates/deletes now add bytes), adjust `maxBytes` in `examples_test.go` for that scenario — bump `over-budget-degraded` / `summary-only` until the invariant (`(hidden to fit size limit)` present; `<details>` absent; etc.) holds, then re-run `-update`.

- [ ] **Step 4: Verify compare mode and the structured example**

Run: `go test ./cmd/tfstackplan -run TestExamples -v`
Expected: PASS. Then confirm the inline-vs-folded structured case rendered:

Run: `grep -n "manifest.ingress\|kubernetes_manifest.configmap\|· 11 paths\|spec.key" examples/big-plan.md`
Expected: `ingress` shows inline `~ manifest.spec.key_NN = old → new` leaves; `configmap` shows a folded `<details>`.

- [ ] **Step 5: Update the README example**

Regenerate the "What it looks like" block from real output. Run the tool on the example fixtures (or copy the relevant section of `examples/big-plan.md`) and replace the live-markdown example in `README.md` so it shows: a create folded with attributes, an aligned update, and a structured-field change. Keep it live markdown (not a fenced block).

- [ ] **Step 6: Full suite + gofmt + vet**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: gofmt prints nothing; vet clean; all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/tfstackplan/ examples/ README.md
git commit -m "examples: structured-string fixture; regenerate goldens; update README"
```

---

## Self-Review

**Spec coverage:**
- Create/delete attributes → Tasks 3 (extract), 4 (leaf), 7 (fold + render). ✓
- Update alignment + names → Tasks 5 (named leaves), 6 (alignment). ✓
- Fractal folding (create/delete + large attrs) → Task 7. ✓
- Fold threshold (~10 lines) → `foldThreshold` const (Task 5); large-attr fold via block detection. ✓
- Structured JSON/YAML example, both inline and folded → Task 10. ✓
- Sensitive/unknown inline → Task 4. ✓
- Size-budget cascade unchanged; render aligns after fit → Tasks 7–8; alignment operates on selected leaves/variants at render time. ✓
- Examples regenerated + budgets retuned + README → Task 10. ✓

**Placeholders:** none — every code step shows the code; budget retuning gives the concrete adjust-and-rerun loop.

**Type consistency:** `model.Field{Name, Leaves, Variants, Selected}`, `model.Leaf{Op, Path, Old, New, Inline}`, `LeafOp`/`OpAdd|OpChange|OpRemove`/`.Sym()`, `Field.IsBlock()/Sel()/AtLast()`, `Change.Fields`, `differ.Diff → model.Field`, `alignLeaves`/`renderResource`/`largestDegradable(*model.Field)` are used consistently across Tasks 1–10.
