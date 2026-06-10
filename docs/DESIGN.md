# tfstackplan — Design Spec

**Status:** Implemented

## Overview

`tfstackplan` renders multiple Terraform `plan.json` files — one per stack, as
produced by a Terramate / multi-root-module CI run — into a single,
reviewer-friendly markdown report suitable for a GitHub PR comment. It fills the
gap between [`tfplan2md`](https://github.com/oocx/tfplan2md) (excellent at one
plan in / one document out) and the reality of monorepos that produce N plans
per PR.

The report has three levels of detail:

1. **Tier summary** — a table of all stacks with action counts and (optionally) a
   classification label, so a reviewer sees scope at a glance.
2. **Per-stack drill-down** — a collapsed `<details>` block per stack.
3. **Diff detail** — the actual resource changes inside each block.

The tool is a **pure renderer**: it scans a directory of per-stack `tfplan.json`
files and writes markdown (and an optional sidecar JSON). It does not run
`terraform plan` and does not post to GitHub — the CI pipeline does that. (It is
growing additional subcommands — a `serve` control-plane and a `run` CI driver;
see *Server foundations* — but `render` stays a pure, standalone renderer.)

## Goals (v1)

- Merge many `plan.json` files into one marker-keyed markdown document.
- Top-level summary table with per-stack action counts.
- Collapsed per-stack `<details>` with a built-in rendered diff.
- **Optional** classification: tag each stack with the **set** of categories it
  matches (multi-label) via a declarative ruleset, primarily to gate IAM changes
  in CI.
- Built-in, configurable presets for common rulesets (`iam` now).
- Comment-size budget handling (GitHub's 65,536-byte cap).
- Sidecar JSON of computed categories for CI gating logic.

## Non-goals (v1)

- Shelling out to `tfplan2md` (deferred; built-in renderer is the v1 path).
- Posting to any VCS, or running `terraform plan`.
- User-customizable output templates.
- Diffing one run against a previous run.
- Multi-tier in a single comment (one invocation = one comment per tier).
- Static-analysis (Checkov/Trivy/SARIF) rollup.

## Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | **Language: Go** | Single static binary; first-party `terraform-json` module for plan parsing; ships cleanly via Homebrew + Docker like tfplan2md. |
| 2 | **Built-in renderer for v1** (no tfplan2md dependency) | No external .NET runtime dependency in CI; fully testable offline; full control of the collapsed-details format. tfplan2md shell-out can be added later if upstream diverges. |
| 3 | **Input: directory scan for `tfplan.json`** | Matches the natural output shape of Terramate scripts and Terragrunt's `--json-out-dir`; no per-run manifest to maintain; stack name is derived from the directory path, not from a config file. |
| 4 | **Classification lives in a separate HCL config file** | Classification is *repo policy* (stable, git-tracked), with a different lifecycle from the per-run manifest. HCL is idiomatic for the Terraform ecosystem (cf. `.tflint.hcl`, terramate's `.tm.hcl`). |
| 5 | **Presets** as built-in named rule bundles | Repos opt into `iam` (and future `data`, `cluster`) without rewriting regexes. |
| 6 | **Multi-label rule evaluation, declaration order is badge display order** | Every matching `preset`/`rule` fires independently — a stack can carry several categories; declaration order determines the display order of the badges (no first-hit-wins). |
| 7 | **No user templating** | Avoid tfplan2md's abandoned templating surface. Classification is a small declarative ruleset, not free-form templates. |
| 8 | **No per-stack manual `class:` override in v1** | Single source of truth for policy (the HCL config). Revisit only if a real need emerges; a sharp custom `rule` usually covers it. |
| 9 | **Functional pipeline: gather → fit → render**, all pure | Build a complete, budget-agnostic model first; reduce it to fit the budget; then render. Each stage is a pure function, independently testable; budgeting is decoupled from both parsing and markdown generation. |
| 10 | **Structural (changed-paths-only) diff for detected JSON/YAML, in v1** | Both a readability *and* a size win — a big manifest with one changed field renders as one line. It's the differ's *preferred* variant, not extra scope. |
| 11 | **Per-attribute `differ` overrides in the `diff {}` HCL block, in v1** | Type detection is heuristic and will misfire; an `(resource_type, attribute) → differ` override is the escape hatch, and nearly free given the differ/fit/render split. |

## Architecture

A **functional pipeline** of three pure stages around a complete intermediate
model. Only the edges touch the filesystem.

```
load (I/O)  →  gather → Model (pure)  →  fit → Model' (pure)  →  render (pure)  →  write (I/O)
```

- **load** — scan `--plans-dir` for `tfplan.json` files, read each one, and load the HCL config.
- **gather** — build a complete, **budget-agnostic** `Model`: per-stack action
  counts, the classification (the set of matched categories), and for every changed attribute an
  ordered list of candidate render *variants* with their byte sizes.
- **fit** — pure `Fit(Model, budget) → Model'`: pick one variant per attribute
  so the estimated total fits, degrading the largest first. Touches diff depth
  only; never the summary table or classification.
- **render** — pure `Model' → markdown`.
- **write** — stdout/file, plus the optional sidecar JSON (taken from the model
  *before* fit, since classification is never reduced).

```
cmd/tfstackplan/   — CLI entry; orchestrate load → gather → fit → render → write
internal/plandir/    — recursively scan --plans-dir for tfplan.json files; derive stack names
internal/config/     — parse + validate the HCL config (classification {} + diff {})
internal/presets/    — built-in named rule bundles (iam, …) as []classify.Rule
internal/plan/       — parse plan.json (terraform-json) → action counts + raw attr changes
internal/classify/   — apply resolved ordered rules → []Category  (gather)
internal/differ/     — type detect + emit ordered render variants per attribute  (gather)
internal/model/      — Model/Stack/Change/AttrDiff/Variant types (the shared spine)
internal/fit/        — pure budget reduction over the model (largest-first degradation)
internal/render/     — Model' → markdown
```

**Key boundaries:**

- `config` + `presets` resolve into a single ordered `[]classify.Rule`.
  `classify` consumes that list and a parsed plan and returns `[]Category` (the
  set of all matching categories, in declaration order) — it neither knows nor
  cares whether a rule came from a preset or a custom block. `Summarize` produces
  the run-level union of categories across all stacks.
- `differ` owns all type-specific knowledge (JSON/YAML/base64/plain) and emits,
  per attribute, an ordered `[]Variant{Level, Bytes, Content}` from preferred →
  minimal. `fit` is generic arithmetic over those variants and knows nothing
  about YAML; `render` just emits the chosen variant. A per-attribute config
  override only changes which variants `differ` emits — `fit` and `render` are
  untouched.

## Inputs

### Plans directory

The tool's only input is `--plans-dir DIR`: a directory that is scanned
recursively for files named exactly `tfplan.json`. Each file found contributes
one stack to the report:

- **Stack name** — the directory containing the `tfplan.json`, expressed as a
  forward-slash path relative to `--plans-dir`
  (e.g. `out/platform/nonprod/tfplan.json` → `platform/nonprod`).
- **Ordering** — stacks are sorted alphabetically by name before rendering.
- **Source dir for links** — `join(--repo-root, stack name)`; `--repo-root`
  defaults to `.` (the working directory).
- **Empty / absent plans** — if no `tfplan.json` files are found, the tool
  renders a header-only "0 stacks changed" report and exits zero.

Background and the orchestrator-integration rationale: `docs/terramate-integration.md`.

### CLI surface

```
tfstackplan --plans-dir DIR
              [--config FILE]                  # HCL policy (classification {} + diff {}); auto-discovers .tfstackplan.hcl in CWD
              [--title TEXT]
              [--marker TEXT]
              [--max-bytes N]                  # default ~60000 (under GitHub's 65536 cap); 0 disables
              [--details auto|open|closed]     # default: closed
              [--emit-classification-json FILE]
              [--repo-root DIR]                # base for link file paths (default ".")
              [--link-var key=value]           # link template var (repeatable)
              [--output FILE | -]              # default: stdout
              [--version]
```

The CLI is now subcommand-based: `tfstackplan render <flags>` is the explicit
form, and a bare flags-first invocation (`tfstackplan --plans-dir …`) still
renders, for backward compatibility. Other subcommands (`serve`, `run`, `state`)
are additive; see *Server foundations*.

- `--plans-dir` is required; it is scanned recursively for `tfplan.json` files.
- `--config` overrides config discovery. If neither `--config` nor an
  auto-discovered `.tfstackplan.hcl` exists, **classification is off** (no
  categories column / icons) and `diff {}` falls back to defaults (detection on, no
  per-attribute cap) — the tool degrades gracefully with zero config.
- `--emit-classification-json` is a no-op when classification is off.

## Plan parsing & action counting

Parse each `plan.json` with the `terraform-json` module. For each entry in
`resource_changes`, reduce `change.actions[]` to a primary action:

| `actions[]`                       | Bucket    |
|-----------------------------------|-----------|
| `["create"]`                      | Add       |
| `["update"]`                      | Change    |
| `["delete"]`                      | Destroy   |
| `["create","delete"]` / `["delete","create"]` | Replace |
| `["forget"]`                      | Forget (removed from state) |
| `["no-op"]` + `previous_address` or `importing` | Noop (move / import only) |
| `["no-op"]` (plain) / `["read"]`  | *ignored* |

**State operations.** A resource is also annotated as *moved* when
`previous_address` is set, and *imported* when `change.importing` is present —
on top of any underlying action (a move can be a pure rename `no-op` or a
move+update). `forget` (the `removed {}` block with `lifecycle { destroy =
false }`) is its own bucket. `Counts` tracks `Move`, `Import`, and `Forget`
alongside the create/change/destroy/replace buckets. Plain `no-op` (no
move/import) and `read` are dropped.

### Summary table

```markdown
<!-- tfstackplan:nonprod -->
### Terraform plan — nonprod  (3 stacks changed)

| Stack                          | Add | Change | Destroy | Categories   |
|--------------------------------|----:|-------:|--------:|---------|
| platform/nonprod                  |   0 |      1 |       0 | 🔐 iam  |
| service-projects/app-dev    |   2 |      0 |       0 | ✅ safe |
| service-projects/app-test   |   0 |      0 |       0 | ✅ safe |
```

- Columns: `Add`, `Change`, `Destroy`, `Replace`. **Any column that is zero
  across all stacks is omitted** (so the common no-replace case shows three
  count columns). Move / import / forget get **no columns**; when present, their
  counts are appended to the Stack cell (`platform/prod · 1 import, 1 move, 1
  forget`).
- The `Categories` column appears only when classification is enabled.
- "(N stacks changed)" counts stacks with ≥1 change, move, import, or forget.

## Classification (optional)

### Discovery

`--config FILE`, else auto-discover `.tfstackplan.hcl` in the current working
directory. Absent → classification disabled.

### HCL schema

```hcl
# .tfstackplan.hcl  (repo policy — checked into git)
classification {
  default {                 # the display fallback shown when a stack matches no rule
    name = "safe"
    icon = "✅"
  }
  # shorthand: `default = "safe"` is equivalent with no icon.

  preset "iam" {            # built-in bundle; expands to its rules at this position
    icon            = "🔐"  # optional: override the preset's default glyph
    emit_attributes = ["project"]

    # Per-resource recovery of an emitted attribute the change does not carry
    # directly (label = the emitted attribute). Optional; repeatable.
    derive "project" {
      resource_type_pattern = "^google_storage_(bucket|managed_folder)_iam_"  # optional; default any
      from_attribute        = "bucket"                                        # required: source scalar
      pattern               = "^(?P<value>.+)-build-cache$"                   # required: capture (named "value", else group 1)
    }
  }

  rule "destructive" {      # custom rule; interleaves by declaration order
    icon                  = "💣"
    resource_type_pattern = ".*"          # optional; default ".*" (any type)
    actions               = ["delete"]    # optional; default: any action
    min_count             = 1             # optional; default 1
  }
}
```

### Rule matcher (deliberately small — no DSL, no booleans)

| Field                   | Meaning                                                                       | Default     |
|-------------------------|-------------------------------------------------------------------------------|-------------|
| `name` (rule label / preset name) | The category name shown in the table                                | required    |
| `icon`                  | Glyph prepended to the category name                                          | none        |
| `resource_type_pattern` | Regex matched against each change's `type` (e.g. `google_compute_instance`)   | `.*` (any)  |
| `actions`               | List of action strings; rule matches a change if **all** listed appear in its `actions[]` | any action |
| `min_count`             | Minimum number of matching changes for the rule to apply                      | 1           |

- A change with `update` matches a rule whose `actions` is unset — so an in-place
  IAM-policy update contributes the `iam` category, not just creates. (The `iam`
  preset leaves `actions` unset by design.)
- Rules with no matcher fields are catch-alls.
- A rule/preset may carry `derive` blocks (see *Sidecar JSON → `derive`* below) to
  recover an emitted attribute a matched change does not carry directly.
- **Classification considers only changes that mutate the real resource**
  (`add`/`change`/`destroy`/`replace`). Pure state operations — `move`, `import`,
  and `forget` — never contribute to any category, because they make no
  apply-time provider write and so need no elevated permission. (The `iam` guard
  exists to gate write-capable applies behind a PAM grant; a move/import/forget
  requires no such grant.) A change that is *both* moved and updated still
  classifies on its underlying `update`. See the PR linked under Known
  limitations for the full rationale.

The result for each stack is the **set of all matching categories**, in
declaration order (display order = declaration order). `default` is shown only
when that set is empty and never appears in the sidecar JSON or summary.

### Evaluation order

Every `preset` and `rule` block is evaluated independently against each stack;
a `preset` block expands to its bundled rules at its declared position. All
rules whose matchers fire contribute their category — a stack can carry several.
Declaration order determines the badge display order. No match → the `default`
is displayed (display-only; never in the sidecar or summary).

### Presets (built-in)

- **`iam`** (v1): one rule matching IAM resource types across providers, e.g.
  `_iam_(policy|binding|member|audit_config)$` for google/google-beta,
  `^aws_iam_`, `^azurerm_role_(assignment|definition)$`. `actions` unset (any).
  Default category name `iam`, default icon `🔐` (overridable via the block's `icon`).
- Future bundles (`data`, `cluster`) follow the same shape; out of v1 scope but
  the `presets` package is structured to add them as named `[]Rule`.

### Sidecar JSON

```bash
tfstackplan --plans-dir out/ --output report.md \
              --emit-classification-json classes.json
```

```json
{
  "stacks": {
    "platform/nonprod": { "categories": [
      { "category": "iam",         "icon": "🔐", "attributes": { "project": ["fh-host-nonprod"] } },
      { "category": "destructive", "icon": "💣" }
    ]},
    "service-projects/app-dev": { "categories": [] }
  },
  "summary": { "categories": [
    { "category": "iam",         "icon": "🔐", "attributes": { "project": ["fh-host-nonprod", "fh-svc-dev"] } },
    { "category": "destructive", "icon": "💣" }
  ]}
}
```

Each stack lists its matched `categories` (`[]` when it matched nothing — the
`default` fallback is display-only and never appears here). `icon` is `null`
when the category has none. `summary.categories` lists every category present
across the run with the per-key sorted-unique union of its attributes — lets a
CI pipeline gate on category subjects without re-parsing the markdown.

**`project` is recovered for net-new GCP resources.** A freshly-created GCP
resource often leaves `project` known-after-apply (it is computed from the
parent), so it never appears as a concrete value. When `project` is requested
via `emit_attributes` but absent, it is derived from a known scalar that
follows the canonical `projects/<project>/…` self-link convention (e.g.
`secret_id` on `google_secret_manager_secret_iam_member`). This keeps
per-project gating (e.g. requesting an access grant for each affected project)
working for brand-new bindings, not only for changes to existing resources
whose `project` is already in state. It never overrides an explicit `project`
nor fabricates one when no such parent path exists.

**`project` also falls back to the stack's project for projectless IAM.**
Bucket- and folder-scoped IAM (e.g. `google_storage_bucket_iam_member`) carry no
`project` and no `projects/<project>/…` scalar to recover one from, so the
per-project gate would see no target. When a rule emits `project` but the matched
changes yield none, it is back-filled from the stack's *unique* project — the
single distinct `project` across all of the stack's changes (any sibling
resource that carries one). Ambiguous stacks (zero or more than one distinct
project) emit nothing, so the gate fails closed rather than guessing; an explicit
value is never overridden.

**`derive` recovers an emit attribute per-resource from another scalar.** The
stack-project fallback above only resolves when the stack touches a *single*
project; it fails closed when a projectless IAM resource lives in a stack that
fans out across several projects (e.g. one CI-pipeline stack granting
`google_storage_managed_folder_iam_member` on `<project>-build-cache` for dev,
test and stage at once — three members, none carrying a project, three sibling
projects → ambiguous). A `derive` block recovers the value from the resource
itself: for each matched change missing the attribute (and matching the optional
`resource_type_pattern`), it reads `from_attribute` and applies `pattern`,
contributing the named `value` capture (else group 1). Being per-resource, each
member resolves to its own project, so the multi-project case yields the full
set of targets. `derive` runs **before** the stack-project fallback and never
overrides a value the change already carries; the repo convention (e.g. the
`-build-cache` bucket suffix) lives in the repo's `.tfstackplan.hcl`, not in the
tool.

## Rendering, the differ, and the size budget

### Why `<details>` is not a size lever

GitHub's 65,536-byte comment limit applies to the raw markdown *source* —
content hidden inside a collapsed `<details>` still counts in full. So
collapsing helps *reviewer ergonomics* (not scrolling past 40 diffs) but does
**not** save bytes. Fitting the budget therefore requires actually
*summarizing or omitting* content, which is what `fit` does.

### Document shape — fractal, per-resource nesting

```
<!-- tfstackplan:nonprod -->     ← marker, always line 1 (CI upsert key)
### Terraform plan — nonprod  (3 stacks changed)
| summary table |
<details> 📁 **stack** ▾           ← folder icon + bold name; closed by default (--details = auto|open|closed)
> <details> ✏️ resource ▸        ← each resource its own row, inside a blockquote
>     N changed                  ← descriptor hangs on an indented line below the address
>   ```diff … ```               ← bar so the stack scope is visible; blank lines pad title/rows
> </details>
</details>
```

Each **stack** is a `<details>` whose heading is a **folder icon + bold name**
(`📁 platform/nonprod · 🔐 iam · 4 change`) so it reads as a section header,
visually distinct from the resource rows nested inside. Its body is wrapped in a
**blockquote** (`>`) so GitHub draws an indented left bar marking the stack
scope; blank quoted lines inside that bar pad the title from the first row and
separate consecutive rows.

**Every resource is its own uniform `<details>` row.** Line 1 is an **emoji
action glyph + the address**, glued with a non-breaking space (`&nbsp;`) so a
long path can't orphan the icon when it wraps. The **descriptor hangs on its own
indented line** below the address (`metaIndent` non-breaking spaces, ≈ glyph
width) so it never collides with a long path. Emoji glyphs (not ASCII) keep
every row icon the same larger size; the diff-body markers inside the
```` ```diff ```` fences stay ASCII `+/-/~` so GitHub still colours them.

- create → ➕ `<addr>` / `N attrs`, delete → ➖ `<addr>` / `N attrs`
- update → ✏️ `<addr>` / `N changed`, replace → 🔁 `<addr>` / `replace`
- moved → ↪️ `<addr>` / `moved from <prev>`, imported → 📥 `<addr>` /
  `imported · id=…` with the id monospaced on the descriptor line
  (`id=<code>…</code>`), forget → ⏏️ `<addr>` / `forgotten · N attrs`
  (state ops take precedence over the underlying action)

**Size-based folding (one rule, all actions):** a resource row is **open** when
its rendered body is small (≤ ~10 lines) and **collapsed** when big. The body
holds aligned scalar leaves (`~ path = old → new`) followed by any structured /
line-diff blocks. `--details open` forces all rows open.

### The differ — leaves and ordered block variants

Each changed attribute becomes a `model.Field`: either **leaves** (aligned
`op path = value` rows — scalars, sensitive, known-after-apply) or a foldable
**block** carrying an ordered variant ladder (preferred → minimal), each with a
precomputed byte cost:

| Value                    | Rendering                                      |
|--------------------------|-----------------------------------------------|
| scalar / sensitive / known-after-apply | a single aligned **leaf** (`~ k = a → b`, `(sensitive value)`, `(known after apply)`) |
| JSON / YAML string, or native HCL map/list | **block:** `Structural` = a *contextual unified diff* of the canonically re-formatted value (2 lines context, `-`/`+`, tagged `~ attr (json|yaml):`) → `Summary` → `Hidden` |
| Plain multi-line text    | **block:** `LineDiff` (limited context) → `Summary` → `Hidden` |
| base64 / binary          | **block:** `Summary` (byte delta) → `Hidden`  |

- **Structured values render as a contextual diff** (not changed-paths) so a
  policy/manifest reads naturally; the value is canonicalized (sorted keys,
  stable indent) before diffing. Diff lines are line-initial so GitHub colours
  the `-`/`+`.
- **Sensitivity is per-path inside a structured value.** Terraform's
  `before_sensitive`/`after_sensitive` is a *tree* mirroring the value, marking
  only the sensitive leaves. A bare `true` (the whole attribute is sensitive) is
  rendered as the `(sensitive value)` leaf; a *nested* marker is carried as a
  subtree and the differ redacts just those leaves before canonicalizing, so a
  single sensitive field (e.g. a container `env` secret) no longer smears
  `(sensitive value)` across an entire `kubernetes_deployment_v1.spec` and hides
  the real (non-sensitive) change beside it.
- **No default per-attribute size cap.** Every attribute starts at full detail;
  the global `fit` pass is the *sole* fit mechanism. A stack whose only change is
  one large attribute is shown in full when there's budget for it, and only
  degraded if the whole document overflows. `max_attribute_lines` is an
  **optional, default-off** readability *ceiling* — when set, the differ won't
  emit a `LineDiff`/`Structural` variant larger than it (it starts at `Summary`),
  for teams that want a hard skimmability limit regardless of remaining budget.
- `Summary` form: `~ content · yaml · 412 lines · 18 changed (hidden to fit size limit)`.
- A `diff {}` block in the same `.tfstackplan.hcl` sets defaults and can force
  a differ for a given `(resource_type, attribute)` when detection misfires:

```hcl
diff {
  # max_attribute_lines = 200   # OPTIONAL readability ceiling; unset = no cap (global fit decides)
  detect = true                 # auto type detection (json/yaml/base64)

  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"   # exact name or glob
    differ                = "yaml"        # auto|structural|json|yaml|line|summary|hide
  }
}
```

  `--max-bytes` (CLI) overrides the document budget; the `diff {}` block carries
  the per-attribute policy. Both are optional — absent means sensible defaults.

### `fit` — global, deterministic budget reduction

`fit` starts every attribute at its preferred variant, measures the assembled
document, and while it is over `--max-bytes` advances the **currently-largest**
attribute one rung lossier. Stable sort (bytes desc, then stack name, then
resource address) makes output **byte-identical across re-runs** of an unchanged
plan — so CI comment upserts don't churn.

```
Fit(model, budget):
    for each attr: attr.variant = preferred (index 0)
    while estBytes(model) > budget:
        a = largest attr not yet at its last variant
        if a == nil: break          # per-attribute degradation exhausted → cascade below
        a.variant = next(a.variant) # Structural/LineDiff → Summary → Hidden
    return applyTerminalCascade(model, budget)
```

The summary table and classification are **never** reduced by this loop; counts
come from action buckets and are independent of how diffs render.

### Terminal cascade (when even all-minimal exceeds the budget)

If every attribute is already at `Hidden` and the document still exceeds the
budget, degrade at the *report* level, each rung emitting a clear error notice:

1. **Summary-only** — drop *all* per-stack `<details>` bodies; keep the full
   summary table; append `⚠️ Per-stack detail omitted to fit GitHub's size
   limit (see CI logs / artifact).`
2. **Minimal summary** — if even the table is too big (e.g. hundreds of stacks),
   replace it with a one-line aggregate
   (`N stacks · A adds · C changes · D destroys · K flagged iam`) plus
   `⚠️ Per-stack table omitted: report needs ~<X> KB, budget <Y> KB.`
3. **Best-effort floor** — marker + minimal line is tiny, so emit it regardless.
   If a pathologically small budget can't hold even that, emit anyway and exit
   non-zero so CI surfaces it.

The marker comment is always line 1 and always survives, at every rung.

`--max-bytes` defaults to a value below GitHub's 65,536 cap (e.g. 60,000) to
leave headroom for any wrapper text the CI adds around the comment; `0` disables
the budget entirely.

## Error handling

- Missing/unreadable `plan.json` for a stack → fail the whole run with a clear
  message naming the stack (a silently dropped stack is worse than a failure).
- Malformed plan JSON → fail, naming the file.
- Invalid HCL config (unknown block/field, bad regex) → fail at load with the
  HCL diagnostic (file:line). Regexes are compiled once at config load so a bad
  pattern fails fast.
- Unknown preset name → fail at config load, listing available presets.

## Testing strategy

- `plan`: table tests over fixture `plan.json` files covering each action,
  replace ordering, and no-op.
- `classify`: table tests feeding a resolved `[]Rule` + a synthetic change set,
  asserting the returned `[]Category` and multi-label / default fallback /
  `min_count` / `actions`-all-present semantics.
- `config`: parse fixtures for the `default` block + shorthand, preset expansion,
  declaration-order interleaving, and each error case.
- `presets`: assert the `iam` bundle matches representative resource types and
  ignores non-IAM ones.
- `differ`: per type (json/yaml/base64/plain/scalar/sensitive), assert the
  emitted variant ladder, that `Structural` is preferred for structured types,
  that with no cap a lone large attribute keeps its full variant, that the
  optional `max_attribute_lines` ceiling (when set) drops the rich variant
  straight to `Summary`, and that a `diff {}` rule forces the configured differ.
- `fit`: pure tests over **synthetic models** (no real plans) — largest-first
  degradation order, determinism (byte-identical re-runs), the
  "one-200-line-fine / ten-collapse" case, and each terminal-cascade rung
  (full → summary-only → minimal → best-effort floor + non-zero exit).
- `render`: golden-file markdown tests — zero-column omission, classification-off
  mode, each variant level, and the cascade notices.

## Shipped since v1

- **Fractal per-resource nesting** — every resource is its own `<details>` row
  inside a per-stack blockquote bar, with one size-based open/closed rule.
- **State operations** surfaced — moved (`↪`), imported (`⤓`), removed-from-state
  / `forget` (`⊘`); counts appended to the Stack cell (no new columns).
- **Contextual diffs** for all structured values (JSON/YAML strings and native
  HCL maps/lists) — canonical reformat, 2 lines of context, `-`/`+` changes.
- **Source-aware links** — header (build/PR/commit), stack (→ dir), and resource
  (→ its `.tf` declaration `file#Lline`) links. URL templates live in a `links {}`
  HCL block; run values come from `--link-var`/`--repo-root`. `internal/source`
  parses each stack's `.tf` (+ `.terraform/modules/modules.json` for local
  modules) to resolve a resource → file:line; unresolvable resources fall back
  to the stack link. The plan JSON carries no source location, so the source
  tree is the input. Deep-linking to the PR diff hunk is not possible (GitHub
  limitation); links point at the block at `{sha}`.

## Known limitations / gotchas

- **`--plans-dir` couples a stack's name to its directory** (name = dir relative
  to the scan root; source-aware links resolve against `repo-root/name`). So the
  plans tree must mirror the stack tree, and names carry whatever prefix the tree
  has (e.g. `stacks/…`). Decoupling name from dir is intentionally not supported.
- **Canonical plan filename is hardcoded `tfplan.json`** — matches Terragrunt's
  `--json-out-dir`; Terramate is scripted to emit the same. A tool that writes a
  different name needs a rename step.
- **`default`/`safe` is a display-only fallback** — rendered for a stack that
  matched no rule, but it **never appears in the `--emit-classification-json`
  sidecar or the summary** (those carry only the matched-rule categories).
- **The run summary unions attributes per category but does not record which
  stacks contributed** them — consumers gate on subjects, not stack lists.
- **A `tfplan.json` at the scan root errors** (no stack name) — plans must live
  in a subdirectory of `--plans-dir`.
- **A *changed* sensitive leaf inside a structured value shows no visible diff
  line.** Per-path redaction replaces the leaf with the same `(sensitive value)`
  marker on both sides, so when *only* a sensitive sub-field changes, its row
  diffs to nothing and the resource appears in the table with no rendered field
  change. This is deliberate (never leak the value); the alternative — a fake
  `(sensitive) → (sensitive)` row — was judged more confusing than useful.
- **Nested *known-after-apply* still collapses to the whole attribute.** The
  per-path treatment added for sensitivity was intentionally not extended to
  `after_unknown`; an attribute with any nested computed leaf still renders as a
  single `(known after apply)`. No correctness/leak risk — just coarser than the
  sensitivity path. Symmetric fix deferred until a real plan needs it.
- **A `move` / `import` / `forget` of an IAM resource is not flagged by the `iam`
  guard.** Classification is mutation-only (see the Rule-matcher section): these
  state operations make no apply-time provider write, so they need no IAM-write
  permission / PAM grant. The notable case is `forget` (`removed {}`), which
  drops a resource from state while leaving the live cloud binding in place —
  real drift (now-unmanaged access) that the guard intentionally does not
  surface, since the apply itself requires no elevated permission. Tracked in
  [PR #9](https://github.com/Fluent-Health/terraform-stack-plan/pull/9).

## Server foundations (in progress)

`tfstackplan` is growing from a pure renderer into a single tool that also
*orchestrates* a multi-stack Terraform CI run — a `serve` control-plane (live
DAG, approval gates, one GitHub check run per environment) and a `run` CI driver
— while Terraform keeps executing in the user's own CI under the user's own
identity. The render core above is unchanged and remains usable standalone.

This increment lands the internal foundations only (no `serve` command yet) —
see [PR #18](https://github.com/Fluent-Health/terraform-stack-plan/pull/18) for
the full reasoning and alternatives weighed:

- **Subcommand CLI.** `main()` dispatches subcommands; `render` is today's
  pipeline verbatim, bare/flags-first still renders, unknown subcommands error,
  `--help` exits 0.
- **`internal/events`** — the typed, versioned runner→server protocol (one
  module, one version): stack `Status`/`Phase` vocabularies, the execution
  `Graph`, and the `Init`/`PhaseEvent`/`Update`/`Finalize`/`GateCheck`/`GateRevoke`
  payloads. Vocabulary is provider-neutral — `environment` (not a fixed tier set)
  and `(class, target)` approval gates (not a single IAM/project gate) — so
  multiple approval classes work without a later protocol change.
- **`internal/store`** — the server's SQLite persistence (pure-Go
  `modernc.org/sqlite`, `goose` migrations embedded via `go:embed`): executions,
  their stack/edge subgraph, and per-`(class, target)` gate state, plus a
  `classified` marker that tells a clean-but-planned PR apart from a never-planned
  one (fail-closed apply gating).

**Watch out:**

- The store is single-writer by design — the forthcoming server is one instance
  per environment (a control plane, not a data plane).
- `executions.status` is the execution-level commit status (written by the serve
  handler), deliberately distinct from the per-stack `events.Status` enum.
- Re-sending an `Init` for the same execution id is idempotent and resets each
  stack's status to its payload value.

**Server core (HTTP API + verdict projection).** On top of the foundations,
the `internal/server` package now implements the control-plane core (still no
`serve` *command* — `App` is constructed directly; the command + config parsing
land later):

- A stdlib `net/http` `ServeMux` (Go 1.22 method routing — no router dependency)
  with a public `GET /healthz` and bearer-authed `POST /api/{init,phase,update,
  finalize}`. An empty configured secret disables auth (local/dev).
- A `GitHub` interface (create/update one check run per environment, post a
  commit status, read PR head SHA) with a `MockGitHub` test double; the real
  client lands in the next increment.
- The verdict as a **pure projection of DB state**: a `snapshot` feeds
  `conclusion()` (check-run conclusion: `""` running → `success` / `failure` /
  `action_required` when a gate is unsatisfied) and `gateStatus()` (link-mode
  commit status). Re-deriving from the DB is race-free and eventually consistent.
- The check-run lifecycle (`ensureCheckRun` idempotent create, `renderAndPatch`,
  link-mode `reconcile`, and a `drive` dispatch) in both **check mode** (rich
  check run) and **link mode** (commit status). `finalize` records the payload's
  `(class, target)` gates as `AWAITING`, marks gated/moving stacks, marks the run
  classified, then drives the terminal conclusion — so a gated plan concludes
  `action_required` and waits.

Still deferred to later increments: the real GitHub client (App JWT), the
SVG/`/live`/`/img` UI (the progress renderer is a minimal seam for now), the
approval backend that flips a gate to `ACTIVE` (`/api/gate/*`, event ingestion,
reconcile loop), and the `serve` command + config parsing.

### Delivery: binary + Cloud Run container

The `serve` face is intended to run as a Cloud Run-class service, so a release
ships **two artifacts from one codebase**: the standalone binary (today's CLI
delivery — Homebrew/Docker) *and* an OCI **container image** whose entrypoint is
the same binary's `serve`. Because the binary is fully static (pure-Go SQLite,
no cgo) and embeds its assets (migrations, and later the UI CSS) via `go:embed`,
the image can use a minimal static/distroless base and needs no runtime files —
`go build` alone always works and consumers never need a CSS/SQLite toolchain.
The release GitHub Action builds and pushes a versioned, multi-arch image to a
public registry (e.g. GHCR) alongside the binary, so a consumer points Cloud Run
straight at `ghcr.io/<org>/tfstackplan:<tag>` with no per-consumer build. The
image build lands in the `serve`-wiring increment (it is only useful once `serve`
exists); Litestream replication and secret mounting are deployment concerns
documented there and in `SECURITY.md`.

## Future / deferred

- `tfplan2md` shell-out renderer behind a `--render` flag.
- Additional presets (`data`, `cluster`, `schema-migration`).
- Multi-part output (split into several comments) as an alternative to the
  terminal cascade's truncation.
- Run-to-run diffing (highlight what changed since the last report).
- Multi-tier in one comment.
- SARIF / static-analysis rollup.
