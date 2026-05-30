# Multi-category classification + run summary

**Date:** 2026-05-30
**Status:** Approved (design)

## Problem

Classification today is **single-label, first-match-wins**: `classify.Classify`
walks the ordered rules and returns the *first* one that fires, so each stack
carries exactly one class. That cannot express a stack that touches several
independently-gated surfaces at once — e.g. a change that modifies IAM **and** a
SQL instance **and** is destructive. A consumer that needs to act per-surface
(request a distinct approval/grant for each) only ever sees the first class and
silently loses the rest.

Two consequences:

- **The per-stack result is lossy.** A stack matching both `iam` and
  `sql-server` reports only `iam`.
- **There is no run-level rollup.** Consumers re-derive "what's the headline
  across all stacks, and which subjects triggered each category" by post-
  processing the sidecar themselves (e.g. `jq 'any(.class=="iam")'` plus an
  attribute union). That logic belongs in the tool, which already has the data.

## Goal

Make classification **multi-label** and add a **run summary**:

- A stack carries the **set** of categories it matched (every rule that fires),
  not just the first.
- The sidecar gains a `summary` object: every category present across the run,
  each with the union of its emitted attributes — the data a CI gate consumes
  directly, no post-processing.

Terminology: the per-result label is a **category** (was "class"). The rendered
column becomes **Categories**. The HCL `classification {}` block name is
unchanged.

## Decisions (locked)

- **`Classify` returns all matching rules**, in rule (source) order. The
  first-match-wins / priority semantics are removed; rules are now independent
  matchers. Each still uses its existing `type_pattern` / `actions` /
  `min_count`; a fired rule contributes a category plus the attributes from its
  own `emit_attributes` (extraction logic unchanged).
- **`default` is a display-only fallback, not a data category.** A stack that
  matches no rules has an **empty** category set. The configured `default`
  (`safe`) is rendered as a badge only when the set is empty. `default`/`safe`
  **never appears in the sidecar or the summary** — categories are exactly the
  matched rules (the things a consumer might gate on). No special-casing needed
  in the summary as a result.
- **Sidecar (`--emit-classification-json`) is reshaped** to
  `{ "stacks": { <name>: {"categories":[…]} }, "summary": {"categories":[…]} }`.
  This is a breaking change to the file shape.
- **Summary** lists every category present across the run, in rule order; each
  carries the per-key **sorted-unique union** of its attributes across all
  stacks that matched it.
- **Rendered `Class` column becomes `Categories`**, showing all matched badges
  in rule order (empty → the default badge). The column still appears iff a
  `classification {}` block exists.
- **No HCL schema change.** The `classification {}` block parses exactly as
  today; only evaluation (all-match) and output (categories) change.
- **No aggregate counts in the summary** (stack totals, action totals). Deferred
  — JSON growth is additive and can land later if a gate needs it.

## Design

### Data shapes

Per-category result (reused for per-stack and summary):

```jsonc
{ "category": "iam", "icon": "🔐", "attributes": { "project": ["p1","p2"] } }
```

- `category` — the firing rule's name.
- `icon` — the rule's icon, or `null` when it has none.
- `attributes` — sorted-unique non-null values per `emit_attributes` key;
  the field is **omitted** when the rule declares no `emit_attributes` or none
  were found (current `extract` behavior, unchanged).

Full sidecar:

```jsonc
{
  "stacks": {
    "platform/nonprod": { "categories": [
      {"category":"iam","icon":"🔐","attributes":{"project":["p1","p2"]}},
      {"category":"sql-server","icon":"🗄","attributes":{"instance":["db1"]}}
    ]},
    "data/warehouse": { "categories": [] }
  },
  "summary": { "categories": [
    {"category":"iam","icon":"🔐","attributes":{"project":["p1","p2","p3"]}},
    {"category":"sql-server","icon":"🗄","attributes":{"instance":["db1","db2"]}}
  ]}
}
```

- Every scanned stack gets a `stacks` entry; `categories` is `[]` for a stack
  that matched nothing.
- `summary.categories` is the union: for each category name present on any
  stack, one entry whose `attributes` merge (sorted-unique per key) every
  matching stack's values. Ordered by rule order. Empty run → `"categories": []`.

### `internal/classify`

- Replace the single-result `Result` with a per-category type and make
  `Classify` return an ordered slice:

  ```go
  // Category is one matched rule's outcome.
  type Category struct {
      Name       string
      Icon       string
      Attributes map[string][]string // nil when nothing emitted
  }

  // Classify returns every rule that fires, in rule order. Empty when none do.
  func Classify(s plan.RawStack, rules []Rule) []Category
  ```

- The matcher loop is unchanged per rule; the difference is it no longer returns
  on the first hit — it appends a `Category` for each firing rule and continues.
- `Classify` no longer takes `def` — the default is a render-time concern.
- `extract` is unchanged.

### `internal/model`

- `Stack.Class *Class` → `Stack.Categories []Class` (matched categories, in rule
  order; empty when none). `Class{Name, Icon}` is reused per category — render
  needs only name+icon, not attributes.
- `Report` keeps `Classified bool`. Add `Report.Default Class` (the configured
  default) so the renderer can show a fallback badge for empty sets.

### `internal/render`

- Column header `Class` → `Categories`.
- Cell: join each matched category as `<icon> <name>` (icon omitted when empty),
  separated by two spaces, in slice order. When `Stack.Categories` is empty,
  render the default badge (`<default.icon> <default.name>`).
- The column is emitted iff `Report.Classified`. Zero-stack report path
  (header-only) is unchanged.

### `cmd/tfstackplan`

- Per stack: `cats := classify.Classify(raw, cfg.Classification.Rules)` returns
  `[]classify.Category`. Two derived uses:
  - **Render:** map each to `model.Class{Name, Icon}` →
    `st.Categories` (drops attributes, which render doesn't use). The renderer
    substitutes the default badge when `st.Categories` is empty, using
    `Report.Default`.
  - **Sidecar:** map each to a `categoryEntry` (name + icon + attributes).
- Build the sidecar in the new shape:
  - `stacks[name] = {categories: <entries>}` for every stack (empty slice → `[]`).
  - `summary.categories` = union across all stacks, grouped by category name in
    first-seen rule order, attributes merged sorted-unique per key.
- Marshal `{stacks, summary}` to the `--emit-classification-json` file. Empty
  run → `{"stacks":{},"summary":{"categories":[]}}`.
- The over-budget / exit-code behavior, links, diff, and `fit` are unchanged.

### JSON field naming

Reserve a small struct set in `cmd` (mirroring the current `classEntry`):

```go
type categoryEntry struct {
    Category   string              `json:"category"`
    Icon       *string             `json:"icon"`
    Attributes map[string][]string `json:"attributes,omitempty"`
}
type stackEntry struct {
    Categories []categoryEntry `json:"categories"`
}
type sidecar struct {
    Stacks  map[string]stackEntry `json:"stacks"`
    Summary struct {
        Categories []categoryEntry `json:"categories"`
    } `json:"summary"`
}
```

`Categories` is a non-nil empty slice for safe stacks so it marshals as `[]`,
not `null`.

## Testing

- `internal/classify`:
  - a stack matching two rules → both categories returned, in rule order, each
    with its own attributes.
  - a stack matching no rules → empty slice (no default substituted here).
  - `min_count` still gates each rule independently (a below-threshold rule does
    not contribute).
- `internal/render`:
  - a stack with two categories renders both badges; column header is
    `Categories`.
  - a stack with no categories renders the default badge.
  - no `classification {}` → no `Categories` column.
- `cmd/tfstackplan` (e2e):
  - two stacks, overlapping categories with different attribute values → sidecar
    has correct per-stack `categories` and a `summary` whose attributes are the
    sorted-unique union; a safe stack shows `"categories": []` and contributes
    nothing to the summary.
  - empty plans-dir → `{"stacks":{},"summary":{"categories":[]}}`.
  - the existing iam/links/no-config e2e tests, ported to the new shape.
- Regenerate the example goldens (the `Categories` column replaces `Class`;
  a multi-category example stack is worth adding to the shared fixture).

## Risks / trade-offs

- **Breaking change, twice over:** the classify semantics (single → multi) and
  the sidecar shape (`{<stack>:{class}}` → `{stacks,summary}`). Acceptable
  pre-1.0; the downstream consumer is being rewritten and must adapt to the
  per-stack reshape regardless. Ship as a minor bump with a CHANGELOG note.
- **`default` excluded from data is a deliberate asymmetry** (rendered but not in
  the sidecar/summary). Documented above; it keeps gating data free of `safe`
  noise.
- **Category badge clutter:** a stack matching many rules shows many badges. Rule
  order controls the sequence; authors keep the set small by writing few, broad
  rules. No truncation in v1.

## Out of scope

- Aggregate counts in the summary (deferred, additive later).
- Per-category stack lists in the summary (consumers need subjects, not stacks).
- Any gating / PAM / VCS vocabulary in the tool — it stays domain-neutral.
- HCL schema changes.
