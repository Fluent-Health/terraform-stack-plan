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
internal/domain/     — canonical value types (Counts, Category) shared across render/wire/persistence; leaf pkg
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

- `domain` is the single home for value types that recur across the render
  pipeline, the runner→server wire protocol, and persistence — currently
  `Counts` and `Category` (the latter carrying `Name`/`Icon`/`Attributes`).
  `model`, `events`, and `classify` re-export them via type aliases
  (`type Counts = domain.Counts`), so each concept has one definition and the
  wire/persistence layers are codecs of it rather than parallel re-modellings.
  This is the first step of the event-sourced target architecture;
  `Status` and the rest follow in later phases.
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

Background and the orchestrator-integration rationale: `docs/guide/01-the-gaps.md`.

### CLI surface

```
tfstackplan --plans-dir DIR
              [--config FILE]                  # HCL policy (classification {} + diff {}); auto-discovers .tfstackplan.hcl (walks up to the repo root)
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

`--config FILE`, else auto-discover `.tfstackplan.hcl` by walking **up** from the
working directory to the repo root (the first ancestor with `.git`; the search
stops there, so a config above the repo is out of scope). This lets a command run
from a stack subdir (`run plan`/`run apply --dir stacks/<tier>`) find the repo-root
policy with no explicit `--config`. Absent → classification disabled.

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
    "platform/nonprod": {
      "categories": [
        { "category": "iam",         "icon": "🔐", "attributes": { "project": ["fh-host-nonprod"] } },
        { "category": "destructive", "icon": "💣" }
      ],
      "counts": { "add": 3, "change": 1, "destroy": 1 }
    },
    "service-projects/app-dev": { "categories": [], "counts": { "add": 2 } }
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

Each stack also carries `counts` — its per-kind operation tally
(`add`/`change`/`destroy`/`replace`/`move`/`import`/`forget`, omitted when zero,
post-state-move overlay). These are plumbed end-to-end as `events.Counts`: from
the sidecar into the `Finalize` event, persisted on the `stacks.counts` column,
and surfaced back via `LoadGraph` on `events.StackState.Counts`. They are
**non-gating display data** (never feed the reconciler `Step`/gate) — the
foundation for the live viewer's operation summaries and the upcoming
blast-radius visualization (Phase 1 of the live-viewer redesign).

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

The config also carries optional **server/serve** blocks for the control plane
(ignored by `render`): `server { url, environment }`; `class "<name>" { backend,
entitlement, entitlement_scope, required }` binding a classification class to an
approval gate (`entitlement_scope` is `projects` by default, or `folders` /
`organizations` for a class that grants at a higher resource scope); and
`serve { db_path, public_base_url, ui_base_url, github_app {
app_id, installation_id, private_key_path }, approval "gcp-pam" { location,
duration, requester_pool } }` for the `serve` runtime. All are optional and
backward-compatible — a render-only `.tfstackplan.hcl` needs none of them.

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
- **Apply gate-check outcomes are typed values, not stringly errors.** The
  server stamps a stable, machine-readable `code` (`internal/codes`, e.g.
  `GATE-001` not-classified, `GATE-002` not-satisfied, `GATE-003` unconfirmable)
  on every gate-check response body; HTTP statuses are unchanged. The runner's
  `GateCheck` maps (transport failure | status | code) into a typed
  `runner.GateVerdict` (`Satisfied`/`NotClassified`/`NotSatisfied`/
  `Unconfirmable`/`Unreachable`) that `run apply` switches on — `Allowed()` is
  true only for `Satisfied`, so an unknown code or an unreachable server fails
  closed. This replaced the prior error-substring matching. The `codes` registry
  is the first step of the target architecture's tagged-error model; it grows as
  later phases convert more boundaries.
- **Unknown stack `status` values are rejected at the wire boundary.**
  `events.Status` validates on JSON decode (`ParseStatus` / `UnmarshalJSON`): an
  unknown non-empty status in any decoded payload fails with a `WIRE-001` coded
  error → HTTP 400, instead of entering the system silently. The empty/unset
  status stays valid, and the store reads status via a direct column cast (not
  JSON), so persisted values are never re-validated.

## Testing strategy

- **Server clock is injectable.** `App.now func() time.Time` (defaults to
  `time.Now` in `New`) is the single seam for the control plane's wall-clock
  reads (apply-lock claim lease/sweep/eval, the live elapsed clock, the log
  janitor cutoff). In-package tests reassign `a.now` to drive claim-expiry and
  janitor-cutoff deterministically — no `time.Sleep`. (Uniqueness/auth time —
  `*-<UnixNano>` ids, JWT exp — deliberately still reads `time.Now`.)
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

- **`run register`** — registers the full stack set up front (before the
  per-stack `run step` invocations begin), so the check-run progress title can
  show `initialized k/N` and `planned k/N` with a known denominator. Without it
  the denominator is `0` until stacks start completing. Runs once at the top of
  the CI job before any terramate script execution. A transparent no-op when
  `TFSTACKPLAN_SERVER` is unset.
- **`run step`** — wraps a single terraform command with lifecycle ticks: ticks
  `running` before (or a custom `--running <status>` — e.g. `--running
  initializing --on-success initialized` for `terraform init`), determines the
  terminal status after, and streams the command's output live as log chunks via
  `/api/logs`. The terminal status is `nochange` when terraform's own summary
  line reads `0 added, 0 changed, 0 destroyed`; the `--on-success <status>` flag
  (e.g. `--on-success safe`) sets the status for any other success. A
  transparent offline passthrough (`run step` is a no-op when
  `TFSTACKPLAN_SERVER` is unset), so local `terramate script run` is unaffected.
  The motivation: terramate's parallel `script run` aborts on the first failing
  stack and never advances to a *later* command in the same job — so a closing
  `run tick --status safe` in a separate command is silently skipped for any
  stack that fails before it. Putting the terminal tick inside the same command
  (`run step --on-success safe -- terraform apply …`) removes that gap. See
  `docs/guide/09-ci-integration.md` for the canonical script form. The companion
  migration of the infra `scripts.tm.hcl` to `run step` is a separate PR in the
  infra repo.
- **`nochange` and `aborted` terminal statuses** — two new `events.Status` values
  extend the per-stack vocabulary. `nochange` means the stack applied
  successfully with zero resource changes (terraform reported `0 added, 0
  changed, 0 destroyed`), set by `run step` from terraform's own output.
  `aborted` means the run failed elsewhere and this stack never reached a
  terminal tick — set server-side by the finalize sweep on `Finalize{Failed:
  true}` for any still-pending or still-running stack. `aborted` stacks are
  correctly distinguished from `failed` stacks (which did run and emitted an
  error); the apply check-run conclusion is now derived from `Finalize{Failed}`
  rather than from counting per-stack failures.
- **Privilege-backed apply** — `POST /api/gate/check` now returns
  `{"requester": "<sa-email>"}` (the leased PAM requester for the PR) on its 200
  success path. `tfstackplan run apply --impersonate-requester` reads that email,
  mints a short-lived `GOOGLE_OAUTH_ACCESS_TOKEN` via the IAM Credentials API
  (using the CI runner's ADC, which must hold `serviceAccountTokenCreator` on the
  pool SAs), and sets the env var so terraform runs **as the elevated requester
  identity**. An unapproved IAM change therefore fails at GCP (403), not only at
  the fail-closed gate pre-check. The flag is a no-op when the gate check returns
  an empty requester (gateless plan, or no server configured) — a gateless apply
  needs no elevation and proceeds as normal. See `docs/guide/09-ci-integration.md` for the
  full wiring and `SECURITY.md` for the hardening notes.
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
- **Per-stack log streaming for plan still requires the script to tee output to
  `--log-file`.** The `LogPump` tails `<stack>/tfstackplan.log` (the default) in
  each stack dir; nothing is streamed unless the stack's terramate script writes
  that file (e.g. `terraform plan ... 2>&1 | tee tfstackplan.log`). This
  indirection exists because terramate's parallel `script run` interleaves every
  stack's output on one stream, which cannot be demuxed per stack. For apply
  steps wrapped in `run step`, the pump is superseded — `run step` streams
  terraform's output directly via `/api/logs` without a file intermediary.
- **PTY mode is Unix-only and merges stdout+stderr.** `run step --tty` opens a
  PTY via `github.com/creack/pty`, which is only supported on Unix. On Windows
  (or if PTY allocation fails for any reason) it falls back gracefully to the
  normal pipe path: logs one line, command still runs, exit code preserved.
  stderr is merged into the single PTY stream (no separate stderr channel).
- **ANSI colour in the viewer requires the consumer to opt in.** The viewer
  parses ANSI SGR codes in stored logs automatically, but terraform only emits
  colour when it detects a TTY — so `run step --tty` (and dropping `-no-color`
  from the terraform flags) is required to get colour. Plans and CLI outputs
  that do not use `--tty` continue to render as plain text.
- **Viewer ANSI rendering: DOM glue is manually verified; the pure parser is
  unit-tested.** `term.js` (the ANSI/CR parser served at `/assets/term.js`) is
  covered by a node test (`web/term.test.cjs`). The DOM glue that connects it
  to the log pane is verified manually. Non-SGR escape codes (e.g. OSC, 256-
  colour sequences) are stripped or passed through unchanged; they are out of
  scope for the viewer.
- **Stacks not reached when a parallel apply aborts are marked `aborted`, not
  `failed`.** On `Finalize{Failed: true}`, the server marks any still-pending or
  still-running stack `aborted` — reflecting that these stacks were never
  attempted rather than that they errored. A stack that did run and fail emits a
  `failed` tick (via `run step`). The apply check-run conclusion is derived from
  `Finalize{Failed}`, not from counting per-stack failures. A stack SIGKILLed
  mid-apply emits no terminal tick and is correctly marked `aborted` by the
  finalize sweep. Failed-stack check-run detail comes from the captured log (the
  real terraform error), not a generic tick string.
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
- **The server's SQLite store enables WAL + `busy_timeout` at the DSN level**, not
  via a one-off `PRAGMA` on a single pooled connection. A background writer (the
  approval `ReconcileLoop`) runs concurrently with the HTTP handlers, and
  `database/sql` opens a pool of connections; setting the pragmas in the DSN
  (`_pragma=journal_mode(WAL)`, `_pragma=busy_timeout(5000)`) is what makes *every*
  pooled connection retry instead of returning `SQLITE_BUSY`. Still single-writer
  by design (one server instance per environment).
- **A classified plan with zero recorded gate targets passes the apply gate.**
  _(Resolved by the reconciler core — see below.)_ `NotClassified` (never planned)
  and `Clean` (classified, zero gate targets) are now distinct sum-type states in
  `GateState`, so a never-planned PR fails closed and a genuinely clean plan passes;
  the old ambiguity where every `gate_targets` write failure was indistinguishable
  from a clean plan no longer applies.
- **An `EXPIRED` grant on a never-active gate target is re-armed on the next tick.**
  A request that lapsed (EXPIRED) before any approval is re-requested by the
  reconcile loop — re-opening the approval window — rather than sitting `Pending`
  until a re-plan. The re-arm is bounded by the PAM request TTL (the fold prefers
  the fresh `AWAITING` over the stale `EXPIRED`, so it does not re-fire every
  tick). A was-active grant that expires still downgrades to `Blocked{expired}`
  (gap ①) within a session; after a restart the gate reloads as `Pending` (the
  flat row can't recover the was-active bit), so it re-requests instead —
  fail-safe (apply stays gated; approval re-opens).
- **The `gcp-pam` grant justification correlates by `environment` (`PR #<n>
  env=<env>`, parsed back with an `env=(\S+)` token), and a non-round-trippable
  environment is now rejected at the grant boundary.** `RequestGrant` validates
  `req.Environment` (non-empty, no whitespace) before any I/O, so a malformed
  environment fails the grant request — the gate stays unsatisfied (fail-closed) —
  rather than silently creating a grant whose correlation reuse/revoke can't
  match. Environments remain slugs (`staging`/`prod`).
- **The `gcp-pam` requester pool leases one SA per (PR, environment), reused
  across all of that PR's gates.** `RequestGrant` picks a pool identity with no
  open grant on the entitlement (falling back to `PR mod pool-size` when
  exhausted), so collision is bounded by *concurrent open PRs exceeding pool
  size*, not by PR-number arithmetic. Once the first grant fixes the identity the
  rest of the PR's grants share it. The leased
  requester lives inside the `GateState` sum type and is carried forward
  structurally by `Step` — the old `store.SetTargetRequester` / `UpsertTarget`
  `ON CONFLICT` carve-out that preserved the requester column is gone; clobbering
  the lease on re-plan is structurally impossible.
- **`run apply` registers an `apply/<env>` execution under a distinct execution
  id.** The plan execution's state is untouched. An `apply/<env>` check run is
  now created on the merge SHA (visible on GitHub's Checks page) and a commit
  status is posted as before (colors the commit-list icon). Per-environment
  `verify/<env>` statuses are not yet distinct (deferred to a later increment).
- **The release image job must lowercase the GHCR repo name.** `github.repository`
  preserves the org's casing (`Fluent-Health/…`), but GHCR rejects uppercase
  (`repository name must be lowercase`). `release.yml` derives a lowercased
  `IMAGE=ghcr.io/${GITHUB_REPOSITORY,,}` env var and tags from it. This first
  surfaced on the v0.6.0 cut (the image job's debut): the binaries built fine but
  the image push failed, so the tag/release had to be recreated on the fixed
  commit — a `release: published` run replays the workflow file *as it was at the
  tagged commit*, so the fix only takes effect once the tag points at it.
- **A grant that expires or a PR that closes *after* the apply gate-check
  returns 200 is enforced by GCP IAM, not the gate-check.** The gate-check `200`
  grants no privilege of its own — the privilege *is* the PAM grant's IAM binding
  on the leased SA. `run apply` mints a GCP OAuth token for that SA
  (`iamcredentials.GenerateAccessToken`) and the apply's writes are authorized
  **per-request by GCP IAM** against the SA's *current* bindings. So if the grant
  lapses or is revoked after the 200, the minted token still authenticates but
  each subsequent Terraform write is **denied by IAM** (the binding is gone) — the
  apply fails partway, with no unauthorized writes. A server-side apply
  lease/token would therefore be **redundant** (it would re-check the same gate
  microseconds later and cannot out-enforce GCP); it was investigated and
  intentionally not built. The one residual nothing server-side can close is GCP's
  own **IAM-propagation delay** (~seconds to a few minutes) after a revocation,
  during which an in-flight apply could still write with the not-yet-removed
  binding. The gate-check itself remains fail-closed on anything observable
  server-side up to the moment it answers.
- **A lost `pull_request.closed` webhook no longer orphans grants forever.** The
  periodic orphan-grant sweep (`OrphanSweepLoop`) is the backstop: within ~5 min
  it revokes grants for any abandoned (closed-unmerged) PR that escaped the webhook
  and the post-apply revoke. Merged PRs are intentionally excluded — their grant
  stays until `ApplySucceeded` releases it (PAM TTL ≤8h if the apply never runs).
  (It still depends on the GitHub API being reachable to learn a PR is closed; it
  is conservative — keeps the grant — on a GitHub error.)
- **GCP PAM exposes no per-grant deep link.** The approval link in the check-run
  summary points at the project-scoped "Approve grants → Pending approval" tab
  (`https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=<target>`).
  GCP does not expose a stable per-grant URL, so reviewers land at the tab and
  must locate their grant by PR/env in the justification. This is a GCP surface
  limitation; no server-side workaround exists.
- **The follow-SSE log replay (`streamLog`) reads only the live buffer — no GCS
  fallback.** A stack that reached a terminal status and had its buffer offloaded
  while the execution is still running (e.g. a slow parallel apply) can return a
  blank stream; the static `GET /logs/<exec>/<stack>` endpoint serves the
  offloaded object and is the correct URL for completed stacks. Not yet fixed
  (see PR: live init-progress & log tweaks).
- **Apply executions store no per-stack plan diff**, so the SPA's Plan tab is
  empty for apply stacks. The per-stack plan section is populated only by the
  plan driver's `Finalize.StackReports`.
- ~~The live page still full-reloads on state change~~ — resolved: the tier
  HTML viewer retired; the central UI's SPA patches the DOM in place on every
  SSE change.

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
  multiple approval classes work without a later protocol change. Per-stack
  `Status` values: `pending`, `initializing` (per-stack `terraform init`
  running), `initialized` (init done), `running` (in-progress), `planned`,
  `safe` (applied with changes), `nochange` (applied, 0 changes), `failed`
  (stack ran and errored), `aborted` (run failed elsewhere; stack never reached a
  terminal tick — set server-side at finalize). During an apply execution, the
  pre-apply re-plan pass maps to **preparing → prepared** (`displayState`
  derives these from the stack's plan-phase status when `PhaseApplying` has not
  yet started); the badge shows **PREPARING** until `PhaseApplying`.
- **`internal/store`** — the server's SQLite persistence (pure-Go
  `modernc.org/sqlite`, `goose` migrations embedded via `go:embed`): executions and
  their stack/edge subgraph, plus **two event-sourced aggregates**. Both are driven
  by a generic `internal/eventsourcing` decider-host (`Load`: snapshot + replay-tail
  via `Evolve`; `Append`: encode + optimistic append + snapshot) parameterized by a
  `Decider[S, E]` value — one engine, two stream scopes:
  - **Gate aggregate** (`exec:<pr>:<env>` stream) — the reconcile gate state:
    `Blocked{reason}`, slot blocker, lease, was-active bit, all folded losslessly.
    `gate_targets` is a **rebuildable derived projection** (cross-PR index for
    `PendingGates`/`OpenGrantPRs`/`PRTargets`), written by the projector and
    regenerable via `RebuildProjection`; never read as truth for the gate verdict.
    Migration `008` dropped `gate_runs` and cleared `gate_targets`; in-flight PRs
    re-plan.
  - **Claim-ledger aggregate** (`env:<env>` stream) — the apply-lock claim state:
    a `ClaimSet` folded over claim/renew/release events. `apply_claims` is a
    **derived projection** (cross-env index for the sweep + UI), written by the
    projector from the folded `ClaimSet`; it is never read as truth for the overlap
    predicate. `held` is a **read-time projection** (`ExpiresAt > now`): expiry is
    enforced at query time, not as a logged event, so the log replays
    deterministically and stale entries are invisible once their lease lapses.
    Migration `009` cleared `apply_claims` at cutover (empty event streams → empty
    projection; in-flight applies re-claim on their next greenlight).

  The `EventStore` boundary: optimistic concurrency on `(stream_id, version)`;
  a PK violation surfaces as `ErrConcurrencyConflict`. The in-process mutex is an
  optimization (elide a DB round-trip when the process serializes writes) but is
  not required for correctness — the DB constraint is the hard boundary.

  **Deployment topology note.** The generic host + stream-scoped + projection model
  supports central (one instance, all tiers), per-tier, and
  commander/viewer (read-only projection consumer) deployments. Streams partition by
  env/tier; nothing cross-tier is atomic. Reaching multi-writer is dropping the
  in-process mutex, not a redesign — the DB constraint already enforces
  optimistic concurrency. No topology configuration is built today; this is a
  documented capability, not a shipped feature.

  See the event-sourced target architecture diagrams above.

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
land later) — see [PR #19](https://github.com/Fluent-Health/terraform-stack-plan/pull/19):

- A stdlib `net/http` `ServeMux` (Go 1.22 method routing — no router dependency)
  with a public `GET /healthz` and bearer-authed `POST /api/{init,phase,update,
  finalize}`. An empty configured secret disables auth (local/dev).
- A `GitHub` interface (create/update one check run per environment, post a
  commit status, read PR head SHA) with a `MockGitHub` test double; the real
  client is now implemented (see *Real GitHub client* below).
- The verdict as a **pure projection of DB state**: a `snapshot` feeds
  `conclusion()` (check-run conclusion: `""` running → `success` / `failure` /
  `action_required` when a human rejected a gate; a gate merely awaiting its
  human keeps the check `in_progress` — waiting is pending, not the red
  `action_required`). Re-deriving from the DB is
  race-free and eventually consistent.
- The check-run lifecycle (`ensureCheckRun` idempotent create, `renderAndPatch`,
  and a `drive` dispatch). Check runs are the only surface — the gate gets a
  rich check run; a post-merge apply gets its own apply check run (falling back
  to a commit status only until that check run exists). `finalize` records the
  payload's `(class, target)` gates, marks gated/moving stacks, marks the run
  classified, then drives the terminal conclusion — so a gated plan concludes
  stays pending with an awaiting-approval title and waits; a denied/revoked/
  expired grant concludes `action_required`. (Gate/finalize state is owned by
  the reconcile
  core; see *Functional reconcile core* below.)

**Real GitHub client** (see [PR #21](https://github.com/Fluent-Health/terraform-stack-plan/pull/21)).
`RealClient` is the production `GitHub` implementation: it mints a short-lived
GitHub App installation token (RS256 JWT signed with the App key → installation
token) per request, and drives the per-environment check run
(`plan/<environment>` — the same name as the commit-status context, so branch
protection requires one consistent context), commit statuses, and PR head-SHA
lookups over the GitHub REST API. The App key (PEM, PKCS#1 or PKCS#8) + app id +
installation id are supplied to `NewRealClient` as values — **no cloud
secret-store dependency in core**; how the deployment obtains them (mounted
secret, env, any provider's secret manager) is a `serve`-wiring concern. The
JWT is hand-rolled with stdlib `crypto` (no JWT library — `go.mod` stays
minimal), and the REST base is overridable so the whole client is tested
offline against an `httptest` fake.

**Live viewer (RETIRED) → central UI.** The tier serves grew an HTML live
viewer over several increments — the `/live` briefing console (header, verdict
band, weighted progress bar, project-grouped stack list, per-stack
Result/Log/Validation tabs with SSE log streaming, approval panel), the `/`
index and `/pr/<n>` timeline pages, and the 30-day view-JWT link auth — all
retired when the central UI (`tfstackplan ui`, above) became the human
surface: its SPA rebuilds those views with in-place SSE updates, eliminating
the viewer's reload-centric bug class by construction. The tier serves no
longer serve HTML at all; `internal/jwtutil` and `serve { webhook_secret_env }`
went with it (the `tfstackplan-token` secret's last job). What the viewer
pioneered still ships through other surfaces: the plan-diff markdown renderer
serves `/plan` fragments (OIDC-scoped; the SPA injects them), per-stack log
reads stream from `/logs` (OIDC-scoped, `Last-Event-ID` resume, GCS-offload
fallback), and the failure-triage + progress helpers feed the check run.

**The DAG image (`GET /img/<id>.svg`, public).** The execution graph renders
as a self-contained, inert SVG (no `<script>`/`<foreignObject>`) so it
survives GitHub's camo image proxy in the check-run body — the one surface
that cannot authenticate, kept public behind unguessable execution ids. Graphs
≤ 40 stacks render per-stack; larger ones fold to the **group** level
(`buildGroupGraph`): stacks group by their first `GroupDepth` path segments
(default 2, or `serve { group { depth | pattern } }` — the regexp's first
capture group wins; invalid patterns fall back to depth), each group node
shows a stack count + worst status + category badges, and edges aggregate to
the group level. `renderGroupSVG` lays groups out in horizontal lanes per
environment sharing one dependency-depth column grid.

**Failure triage — "Needs attention" (Phase 4).** A failed stack's bare error is
turned into an actionable triage by one pure classifier, `classifyFailure(detail,
cats) → failureTriage{Class, Cause, Steps, StateImpact, Retryable}` in
`internal/server/triage.go`. It is a **bounded, ordered pattern set** (`failureMatchers`)
— `state_move` (matched first, so a move failure carrying an embedded lock needle
still reads as a move), then `quota` before `iam_denied` (both can match `error 403`,
so the more specific `quota exceeded` wins), then `state_lock`, `already_exists`,
`provider_auth` — first match wins; an unmatched error falls back to `Class:"error"`
with an **empty `Cause`** (the raw error is shown, never a fabricated guess) plus
generic recovery steps. The **check-run** `failuresSection` (`render.go`)
renders cause + next steps + state impact as GitHub Markdown inside the
per-stack `<details>` (suppressed when there's no captured detail — the "see
the build log" note is then the only honest advice); the retired live page's
triage cards live on as a rebuild target for the central UI's execution view.
Grow the matcher set with a row + a test case, never a heuristic DSL. The
deeper "grant expired at HH:MM" correlation (needs grant timestamps) is
intentionally out of scope.

**Colored live logs.** `run step --tty` runs the wrapped terraform command under
a PTY (`github.com/creack/pty`) so terraform emits ANSI colour — terraform
suppresses colour when its stdout is a pipe, so a PTY is required. If PTY
allocation fails (Unix-only; any OS-level error) the flag is silently ignored and
the command runs via the normal pipe path (logs one line, exit code preserved).
PTY mode merges stdout and stderr into a single stream. The streamed log is stored
**raw ANSI** so the viewer can render colours without re-encoding. At the two
surfaces that cannot render ANSI — the outcome-classification regex
(`classifyStep`) and the failed-tick detail, and the GitHub check-run
`errorTail` — `internal/ansi.Strip` removes SGR codes before the text is
consumed. The viewer's ANSI/CR parser was extracted to a served, unit-tested
`internal/server/assets/term.js` (at `/assets/term.js`); it gained carriage-return
(`\r`) handling so terraform progress spinners (`Still creating… [10s]`) animate
and collapse to their final frame rather than printing every update as a new line.
A node test (`web/term.test.cjs`) covers the pure parser. Consumer wiring — adding
`--tty` to the `run step` commands AND dropping `-no-color` from the terraform
flags — lands in a companion PR in the infra repo.

**serve memory footprint — heap-spike reduction.** `tfstackplan serve` was
OOM-killed until its limit was raised to 2GB; its RAM scales with terraform
log/plan volume, not "just JSON". Two Go-heap spikes were cut. (1) The GCS log
offload now **streams** the file to GCS — `gcsObjectStore.Put` sets
`Content-Length` from the `*os.File` and passes it as the request body — instead
of `io.ReadAll`-ing a whole stack log into memory at finalize. (2) Per-stack
plan markdown renders **on demand**: `GET /plan/{exec}/{stack...}` (OIDC-scoped
since the viewer retirement) renders one stack's plan to HTML when the central
UI's Plan tab opens it, so nothing ever renders every stack's plan section at
once (the whole-execution report is still rendered once). Residual, by deployment design: the SQLite DB and
the live per-stack log buffers still live on the 256Mi `medium=MEMORY` (RAM) tmpfs
volume — moving logs off RAM entirely (a bounded in-memory ring + incremental GCS
upload) is deferred. The 2GB→1GB walk-back is a separate infra decision.

**Check-run richness (Phase 5 of the live-viewer redesign).** Both the plan and
apply check-run summaries now lead with a **blast-radius headline** (op-count
tally: `+add ~change ±replace −destroy ↔move`), **verdict chips** (`⚠️ Destructive`
/ `⚿ k IAM`), and a **per-stack table** (Stack | Ops | Risk | State) with a link
to the live viewer. The renderer is a shared `checkSummary(kind, environment,
stacks, viewerURL)` function in `internal/server/render.go`, consumed identically
by both the plan and apply drivers; the previous `renderProgress` task-list
summary is retired. The apply check run is now correctly titled **"Terraform
apply"** (`CheckRunUpdate.Title` is emitted by the real client and set per
driver); previously it was mislabelled "Terraform plan". The check-run progress
title includes an **init band** with a known denominator N once `run register`
registers the stacks; the label reads `initializing N stacks…` until at least one
stack completes init, at which point it becomes `initialized k/N`. For an apply,
the title reads `preparing k/N` during the pre-apply re-plan pass (which runs
under a pre-apply phase before `PhaseApplying`), flipping to `applying k/N` once
the real apply begins; `progressTitle` takes a `kind` arg and the bar rendering
is factored into `progressBar`. The preparing pass uses the *same* unified
weighted bar as the rest of the apply — its warming + initializing bands fill and
flow continuously into the applying band; only the label (not the bar fraction)
is special-cased to "preparing". (Earlier it rendered a separate `prepared/total`
scale that sat at 0% during warming/init and then jumped to the applying band —
[PR #101](https://github.com/Fluent-Health/terraform-stack-plan/pull/101).) The title is `bar · label` — the bar is the
weighted overall fraction and the label carries the per-phase count, so it does
**not** repeat the count between them (the old `bar k/N · applying k/N` double
indicator) ([PR #99](https://github.com/Fluent-Health/terraform-stack-plan/pull/99)). The rendered report shown in the check-run
**details** uses `render.RenderNoTable` —
header + per-stack change trees, **omitting** the leading summary table, since the
per-stack view already covers the overview; the full report (with the
summary table) is still emitted to stdout / the `render` CLI for PR-comment
output (`run()` returns both variants; `classifyResult.ReportNoTable`; the apply
path gets the no-table report through the `classifyForGateFn` seam). PAM
approval links in the check summary point at
`https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=<target>`
— the "Approve grants → Pending approval" tab — which is the finest-grained
stable URL GCP exposes (see Known limitations).

**Approval gate** (see [PR #23](https://github.com/Fluent-Health/terraform-stack-plan/pull/23)).
`internal/approval` defines the provider-neutral gate abstraction: a `Backend`
(`RequestGrant`/`ListGrants`/`Revoke`) over a `Request{Class, Target, PR,
Environment}` and a normalised `GrantState` (AWAITING → ACTIVATING → ACTIVE, plus
DENIED/REVOKED/EXPIRED). The server only ever *requests*; humans approve in the
backing provider. An in-memory `Fake` makes the whole gate flow e2e-testable.
The server wires it via an optional `App.Approval` field (nil disables gating —
gates park pending with the awaiting-approval title): at finalize it requests a grant per `(class,
target)` gate and records the grant name + state; `reconcileGate` refreshes each
target's state from the backend and, once all are `ACTIVE`, flips the gated
stacks safe and re-drives the check run to `success`; a periodic `ReconcileLoop`
runs that over `PendingGates`, self-healing the activating→active transition with
no provider event required. A periodic **orphan-grant sweep** (`OrphanSweepLoop`,
~5 min) backstops the close-webhook: it lists every PR still holding an open grant
— including fully-ACTIVE (Satisfied) gates the 30s reconcile loop skips — checks
each PR's GitHub state, and revokes (via the same `revokeOrphans` path) any whose
PR is abandoned (closed-unmerged). It is conservative on a GitHub error (keeps the
grant) and idempotent.

**Orphan-grant revocation** — the close-webhook, the periodic `OrphanSweepLoop`,
and the slot-collision auto-revoke — targets **abandoned** (closed-unmerged) PRs
only, via `gh.PRAbandoned` (`closed && !merged`) and the webhook's
`pull_request.merged` field. A **merged** PR's grant is left for its post-merge
`run apply` — released by `ApplySucceeded`, with the PAM request TTL (≤8h) as the
backstop if the apply never runs. (Previously, revoking on any close — including a
merge — could kill a merged PR's grant out from under its pending apply.)

The apply path uses `POST /api/gate/check`
(**fail-closed**: 200 only when the PR was classified *and* every gate target is
`ACTIVE` — a clean classified plan with zero gates passes; a never-planned PR
fails closed) and `POST /api/gate/revoke` (best-effort post-apply cleanup). The
verdict stays a pure projection of `gate_targets`; the backend only changes *who
writes* the `ACTIVE` state.

**`gcp-pam` backend** (see [PR #24](https://github.com/Fluent-Health/terraform-stack-plan/pull/24)).
`internal/approval/gcppam` is the first real `approval.Backend`, over GCP
Privileged Access Manager: `RequestGrant` (create-or-reuse — lists the
entitlement's grants and reuses an open one matching the change, else creates),
`ListGrants` (maps PAM state → normalised `GrantState`, parses `(PR, environment)`
back from the grant justification to correlate), and `Revoke`. Everything
deployment-specific is `Config` — per-class entitlement ids, the requester
service-account pool, location, duration — so the package has **no hardcoded
names**. Grant *creation* impersonates the leased requester identity (PAM
elevates the requester, and a pool avoids elevating every concurrent workload
that shares one identity); *list* and *revoke* use the server's own ambient
credentials (the requester SA lacks revoke). GCP credential acquisition is
**injected** (`TokenFunc`/`ImpersonateFunc`), so the package is dependency-free
and tested offline against an `httptest` PAM fake — the real ADC + impersonation
funcs are supplied by `serve`.

**`serve` command + config + Cloud Run container** (see [PR #25](https://github.com/Fluent-Health/terraform-stack-plan/pull/25)).
`tfstackplan serve` ties the server together: it parses the config (the
`server {}`, `serve {}` — with `github_app {}` + `approval "gcp-pam" {}` — and
`class "<name>" {}` blocks, all backward-compatible so a render-only file is
unaffected), opens the SQLite store, builds the real GitHub client (App key from
a mounted file) and the gcp-pam backend (real ADC + impersonation credentials),
sets `App.Approval`, starts the reconcile loop, and serves. The binary is static
and embeds its assets, so the release workflow builds a distroless, multi-arch
container and pushes it to GHCR alongside the per-platform binaries; its
entrypoint is `serve`. Deployment notes (single instance, Litestream, identity)
are in `docs/reference/install-and-deploy.md`; the hardening notes are in `SECURITY.md`.
With this, **Phase 1 is complete**: render + serve (live DAG, approval gates,
one check run per environment) ship from one binary.

**Phase 2: the runner (`run`, in progress).** The `run` subcommand is the CI
driver that replaces the per-stack bash glue: it wraps the same `terramate
script run` a human invokes, detects the changed set, runs plan/apply,
captures per-stack output, renders + classifies in-process, and reports the
execution lifecycle to the `serve` control plane. The first increment landed the
**runner event client** (`internal/runner`): a typed client over the Phase-1
`events` protocol that posts `init`/`phase`/`update`/`finalize` and
`gate revoke` best-effort (a down or absent server degrades the build to "no
live progress", never to failure — an empty server URL is a full no-op, so local
runs and the no-op `run tick` need no server) and an apply-time `gate check`
that is **fail-closed** (it passes only on a satisfied gate; a 409, any non-2xx,
or an unreachable configured server blocks the apply). The second increment
landed **`run tick`** (under a `run` subcommand group): the internal per-stack
reporter the terramate scripts call — it reads the execution context from the
`TFSTACKPLAN_*` environment (server URL, token, execution id, stack) the
orchestrator sets, posts a best-effort `update`, and is a no-op offline and on
any server error, so a human's `terramate script run` is unaffected and a tick
never fails the build. The third increment landed the **terramate exec layer**
(`internal/runner`): a `Terramate{Bin, Dir}` adapter that shells out to the
`terramate` binary (`cmd.Dir = Dir`, so asdf resolves the project's
`.tool-versions` and terramate uses it as root) to list stacks, detect the
changed set (`list --changed -B <ref>`), derive the dependency DAG for the
server graph (`experimental run-graph -l stack.dir` → a pure DOT parser →
`events.Edge`s), and run a terramate script across stacks (`script run`
[--changed/--parallel/-B]). It is tested against **real terramate** via a
vendored fixture project + a git-init harness (the suite skips cleanly when
terramate isn't installed; a repo `.tool-versions` pins terramate 0.17.0 /
terraform 1.13.3 so it runs in CI).

The fourth increment landed **`run plan`** — the CI plan driver: it detects the
changed stacks, registers the execution + DAG on the server (`Init`), runs the
terramate `plan` script across the changed set (setting the `TFSTACKPLAN_*` env
so the script's `run tick` reports per stack), gathers each stack's `tfplan.json`,
renders + classifies them **in-process** (reusing the render core), derives the
approval gates (each gating `class` binding × its emitted target values) and the
moving stacks from the classification sidecar, and posts `Finalize`. Server
reporting is best-effort (the report always renders, so a local run is useful);
a plan-script failure still finalizes the plans that exist and marks the run
failed. Tested end to end against real terramate + a stub `terraform` (recorded
plan JSON).

The fifth increment landed **`run apply`** — the CI apply driver. Over `run
plan` it adds a **fail-closed gate pre-check**: it asks the server whether the
PR's approval gates are satisfied *before touching terramate* and refuses to
apply otherwise (a 409, any non-2xx, or an unreachable *configured* server
blocks; an unconfigured server is a no-op pass — nothing gates). It then
registers the apply execution, applies the changed stacks via the terramate
`apply` script (terramate honoring the dependency DAG; `--parallel N` runs
independent stacks concurrently, default serial), and revokes the PR's grants
afterward (best-effort, whether or not the apply succeeded). **`--parallel N` is
also threaded into the pre-apply classify/re-plan pass** (`classifyForGate`) —
otherwise that re-plan of every changed stack runs strictly serially and
dominates a large tier-apply, even though the apply step itself is parallel.
Tested end to end
against real terramate + a stub `terraform`: gate satisfied → apply runs in DAG
order + revoke; gate blocks → abort before any apply. The `--impersonate-requester`
flag (see *Shipped since v1 — Privilege-backed apply*) is additive on top of this
increment; the gate pre-check behaviour is unchanged with the flag on.

The sixth increment landed the **CI integration guide** (`docs/guide/09-ci-integration.md`):
the consumer-facing wiring — the `TFSTACKPLAN_*` environment, the terramate
`plan`/`apply` scripts that run terraform + `run tick`, and example plan-on-PR /
apply-on-merge CI jobs that each shrink to checkout + one `run` invocation. With
this, **Phase 2 is complete**: `tfstackplan run` drives plan/apply end to end,
reporting to the `serve` control plane, while terraform keeps executing in the
consumer's own CI under their own identities.

**Native provider caching** (`run plan` / `run apply`) — both drivers support a
`cache {}` config block that pre-warms the local Terraform plugin cache directory
(`TF_PLUGIN_CACHE_DIR`, defaulting to `/workspace/.tf-plugin-cache`) from GCS
before `terraform init` runs. Providers found in GCS are restored directly from
the archive; providers missing from the cache are downloaded from the Terraform
registry (or the provider's own registry host for non-public providers) and
installed. After the script completes, any newly installed providers are uploaded to
GCS so subsequent runs hit the cache. In `run apply` the warm pass runs **before
the classify pass** (the pre-apply re-plan) — not merely before the apply script.
The classify pass itself runs the terramate `plan` script: parallel `terraform
init`+plan across stacks. Since terraform's shared plugin cache is not
concurrency-safe to *populate* (see Known limitations), warming must complete
before any parallel init, or those inits race on a cold cache. Warm therefore
runs ahead of the gate pre-check too — it only downloads providers to a local
directory (never mutates state), so it is safe before the gate, and a
`PhaseWarming` event is emitted at that point.

Configuration in `.tfstackplan.hcl`:

```hcl
cache {
  bucket  = "my-tf-plugins-cache"   # GCS bucket (also: TFSTACKPLAN_CACHE_BUCKET env)
  prefix  = "infra/tf-plugins"      # GCS key prefix (default: "infra/tf-plugins")
  version = "v1"                    # cache key namespace (default: "0"); bump to bust
}
```

Absent → caching is skipped entirely (no GCS access, no extra latency). GCP
credential failures are logged and the run continues without warming — apply is
never blocked by a cache miss; terraform's own `init` download handles it.

**Implementation:** `internal/cache` — `ProviderCache.Warm` fans out up to 8
concurrent per-provider restores, each atomically placed via `os.Rename` into
`<cacheDir>/<address>/<version>/<os_arch>/`; `ProviderCache.Save` walks the
cache directory and uploads any provider not yet present in GCS as a `.tar.gz`.
GCS key format: `{version}/{address}/{provider_version}/{os_arch}.tar.gz`. Archive
extraction guards against zip-slip (path-traversal) for both `.tar.gz` and `.zip`
archives.

**Known limitations:**
- GCP credentials (ADC) are required; a machine without credentials silently skips
  warming and lets terraform's own `init` download handle providers.
- `Save` buffers each provider archive in memory (`bytes.Buffer`) before uploading;
  a directory with many or very large binaries can spike RAM at finalize time.
  Streaming tar-to-GCS is a follow-on.
- The cache is per-`(version, os_arch)` — provider archives for different platforms
  are stored independently.
- Concurrent `Warm` calls for the same provider (e.g. two CI runs sharing a cache
  directory) are safe: `restoreOne` uses `os.Rename` for an atomic install, and if
  the rename fails because a concurrent goroutine already won the race, it verifies
  the binary is present before returning success. The `Save` path has a TOCTOU
  window on the GCS `Exists` check, so a provider may be uploaded twice in a tight
  race; this is harmless (idempotent PUT).
- Terraform's shared plugin cache (`TF_PLUGIN_CACHE_DIR`) is **not concurrency-safe
  to populate**. Parallel `terraform init` over a *cold* cache lets N inits race to
  download the same provider into the same directory, and one stack ends up with a
  package whose `h1:` dir-hash matches no lock entry (`Failed to install provider
  from shared cache … doesn't match any of the checksums recorded in the dependency
  lock file`). The warm pass exists to avoid this: once warm, every parallel init is
  a pure cache read (hardlink). This is why warm must precede *every* parallel init,
  including `run apply`'s classify pass — not just the apply script (v0.18.1, PR
  #145). The failure signature is plan-green / apply-red on an identical commit,
  lock, and cache, failing on one of N parallel stacks.
- Cached provider packages include the provider's `LICENSE.txt` alongside the
  binary, so the package `h1:` dir-hash legitimately differs from the registry-zip
  hash. A lock file generated purely from the registry (`terraform providers lock`)
  still validates against the cache as long as it records the cache's `h1:`; do not
  try to "fix" a cache-hash mismatch by clearing the cache — re-downloading yields
  the same package.

**Phase 3: logs + UI v2 (in progress).** The first increment landed the
**server-side log pipeline foundation**: `POST /api/logs` (bearer-authed)
ingests per-stack output chunks (`events.LogChunk`), the server appends them to
per-stack on-disk buffers under a configured `LogsDir`, and a tail excerpt
(~16 KB) is mirrored into the `stack_outputs` table (pointer + excerpt per stack
per kind — the pointer is set later on object-store offload). A public
`GET /logs/<exec>/<stack>` streams the buffer (so viewers need no cloud IAM, like
`/live`); untrusted path components are sanitized and containment-checked against
`LogsDir`. Log ingestion is disabled (a no-op) when `LogsDir` is unset.

The second increment landed **SSE log streaming**: an in-process fan-out `hub`
(keyed `exec|stack`, non-blocking publish — a slow viewer drops chunks while the
buffer + offload keep the full record) into which `appendLog` publishes after the
durable write; `GET /logs/<exec>/<stack>?follow=1` upgrades to Server-Sent Events
— it subscribes *before* replaying the buffer (so no chunk is missed between
replay and live), then streams live chunks until the client disconnects (the
deferred `unsubscribe` prevents subscriber leaks).

The third increment landed **object-store offload**: an `ObjectStore` interface
(`Put`/`Get`) with a containment-checked filesystem impl (`FSStore`, for
tests/local; a GCS impl is wired by `serve` for deployment, mirroring the
`gcppam` injection pattern), exposed as the optional `App.Objects` field (set
after construction, like `Approval`). When a stack reaches a terminal status,
`handleUpdate` calls `offloadLog`, which uploads the full buffer under
`executions/<exec>/<stack>/log` and records that key as the
`stack_outputs.pointer` (preserving the tail excerpt). The public static
`GET /logs/<exec>/<stack>` now prefers the live buffer and falls back to
streaming the offloaded object via the pointer (`io.Copy`, not slurped), so a
completed execution's logs survive buffer cleanup with no cloud IAM for viewers.
The **follow (SSE) path** gets the same fallback ([PR #99](https://github.com/Fluent-Health/terraform-stack-plan/pull/99)):
when the live buffer is absent (the run concluded — offloaded + deleted at
finalize), `streamLog` replays the offloaded object (`streamOffloaded`, resuming
from the byte offset) and emits a terminal `event: done` so the client closes
instead of auto-retrying a buffer-less stream. Without this, a viewer left open
across finalize re-opened follow connections that replayed nothing and hung —
the "log briefly shows then disappears" bug (the central UI's log tab rides the
same stream + fallback). With
`App.Objects` unset, behavior is unchanged (buffer-only; offload no-ops so the
buffer survives and the follow path still replays it). _Remaining log
limitation for the UI sub-plan: the SSE **replay** still reads the buffer into
memory with no mid-replay cancellation, and should stream from disk/object-store
with context cancellation; a periodic SSE heartbeat + reconnect
(`id:`/Last-Event-ID) are also wanted for the UI Log tab behind proxies._

The fourth increment landed **runner-side per-stack log capture**: the `run
plan` / `run apply` drivers tail each changed stack's on-disk log file (default
`tfstackplan.log`, overridable via a `--log-file` flag) and stream the appended
bytes to `/api/logs` live (~2s tick) through a `LogPump` (`internal/runner/
logpump.go`), with a final flush at run end. It is best-effort: with no server
configured (`client.Enabled()` false) or an empty `--log-file`, no pump runs.
The convention is that each stack's terramate script tees terraform output to
that per-stack file in the stack dir (see Known limitations).

The fifth increment landed **per-stack plan storage**: `handleFinalize` now
iterates `events.Finalize.StackReports` (stack path → rendered plan section,
populated by the runner) and calls `store.UpsertStackOutput(id, stack, "plan",
"", md)` for each entry — storing the full/pre-fit markdown inline in the
`excerpt` column (pointer unused). The stored section is retrievable via
`store.GetStackOutput(id, stack, "plan")` and will feed the per-stack Plan tab
in the UI v2 (PR #TBD). This loop runs in both the failed and non-failed paths
(it executes before the `f.Failed` early-return branch).

The sixth and final UI-v2 increments added the per-stack detail tabs and the
index/PR-timeline navigation pages — since retired with the viewer; their data
paths live on as `store.ListExecutions`/`store.ListExecutionsForPR` behind
`GET /api/executions` and the `/logs` SSE stream (id-tagged, `Last-Event-ID`
resumable, heartbeat-kept, buffer replay on reconnect) that the central UI's
Log tab consumes.

The seventh increment landed **production log-offload wiring**: the
`serve { logs_dir, objects { backend = "gcs"  bucket  prefix } }` config block
sets `Config.LogsDir` (the per-stack buffer dir) and, when `objects` names a GCS
bucket (`backend` empty or `"gcs"`), points `App.Objects` at a dependency-free
GCS object store (`cmd/tfstackplan/gcsobjects.go`: the JSON API over plain
`net/http` with an ADC bearer token, mirroring the `gcslock` injection pattern;
objects live at `<prefix>/<key>`). Completed-stack logs offload to GCS and serve
via the stored pointer once the on-disk buffer is gone; `FSStore` remains for
tests/local. `buildServeApp` reuses the injected `creds` factory for the token.

**Pub/Sub push ingestion** is now wired. `POST /pubsub/push` ingests Google
Pub/Sub push deliveries: the bearer token is OIDC-verified via `idtoken`
(audience defaults to `<public_base_url>/pubsub/push`, or is set explicitly);
the verified email is checked against the configured `service_account`. On
success it triggers a targeted `drive` if the message carries an execution `id`,
else `reconcilePending` — a latency optimization over the polling `ReconcileLoop`,
which remains the fallback. Configured via `serve { pubsub { audience
service_account } }`; disabled (404) when the block is absent.

The `gcp-pam` requester pool now uses **true leasing**: `RequestGrant` picks a
pool identity with no open grant on the entitlement (the open grants are the live
leases), falling back to the `PR mod pool` slot only when the pool is exhausted —
replacing the prior pure-modulo slot. The `state` subcommand (Phase 6) is now feature-complete for
moves: SP1 (same-stack moves) and SP2 (cross-stack moves via native
import/removed) shipped, and SP3 adds the lock-less `state mv` executor
(`state move --via mv` + `state apply`). See *`tfstackplan state` (Phase 6)*
below.

The eighth increment landed **Live UI v2 — P1 bug fixes** (see PR #63):

- **`logs_dir` now always resolves.** When `logs_dir` is unset in the config, `serve`
  derives it from `db_path` (`<db-dir>/logs`), so log ingestion can never be
  silently disabled. An explicit `logs_dir` still wins. Startup logs the resolved
  path. An infra companion PR adds an explicit `logs_dir = "/data/logs"` to the
  Cloud Run config.
- **Log buffer lifecycle.** At finalize, after every stack's buffer is successfully
  offloaded to the object store, the execution's buffer directory is deleted (the
  in-memory `/data` volume on Cloud Run is small). A startup janitor removes
  orphaned buffer dirs older than 24 h. Buffers are never deleted without a
  successful offload (the `errNoObjectStore` sentinel prevents deletion when no
  store is configured).
- **Finished executions serve logs statically.** The stack detail page detects
  whether the execution has concluded (`isFinished`) and, if so, fetches the log
  from `/logs/<exec>/<stack>` (which falls back to GCS) via a plain `fetch()`
  instead of opening an SSE stream. Live executions continue live-tailing as before.
- **Per-stack Plan tab renders as HTML.** The stored per-stack plan section is now
  piped through the shared goldmark pipeline before being served; `stack.gohtml`
  renders it inside a `tfsp-report`-styled container instead of a raw `<pre>`.
- **GFM-style diff colorization.** A goldmark `diffCodeRenderer` overrides
  ` ```diff ` fenced-code blocks: each line is wrapped in a `<span>` with
  `diff-add` / `diff-del` / `diff-chg` classes (GitHub dark-palette colors via
  `report.css`). Non-diff fences reproduce goldmark's default output.
- **Shared `report.css`.** Report and approval-panel styles moved from
  `live.gohtml`'s inline `<style>` into a committed `assets/report.css` file
  served at `/assets/report.css` and linked by both `live.gohtml` and `stack.gohtml`.
  The diff palette lives there too.
- **`apply/<env>` check run (initial wiring).** `handleInit`/`handlePhase` now call
  `ensureCheckRun` for apply contexts (in addition to the existing gate-context
  path), creating a check run named after the status context (e.g.
  `apply/nonprod`). `driveApply` updates that check run with progress and a
  terminal conclusion. The existing commit status is preserved alongside it.
  (The rich blast-radius summary and correct "Terraform apply" title landed in
  Phase 5 of the live-viewer redesign — see *Check-run richness* above.)

**Phase 4: `run verify` (complete).** `tfstackplan run verify` runs the
terramate `verify` script across changed stacks — no gate, read-only
post-apply validation. It streams per-stack logs via the tail-pump (same
`LogPump` as plan/apply) and registers a `verify/<env>` execution on the
server, reported under a `PhaseVerifying` phase that joins the execution
timeline. The latest verify run is exposed as `verify_execution_id` on the
execution read (the central UI links/tails it over the same `/logs` SSE
stream), resolved via
`store.LatestVerifyExecutionID(db, pr, env)` and its per-stack log excerpt via
`store.GetStackOutput(db, verifyExec, stack, "log")`; when no verify run exists
yet the tab shows "No verify run yet for this PR."

**Phase 5: multi-class approval (complete).** Approval is multi-class: each
`class "<name>" {}` block binds a classification class to a PAM `entitlement`
and an optional `entitlement_scope` (`projects` by default, or `folders` /
`organizations`), threaded through `config.ClassBinding` and into the gcp-pam
`Config` (`Entitlements` + `EntitlementScopes`, keyed by class). A second
approval class (e.g. `database`) therefore gates independently at its own scope
without touching the IAM gate. No new machinery was needed: the gate, verdict,
and UI are already `(class, target)`-generic — only the config surface and
serve wiring grew.

### Reconciler core

`internal/reconcile` is the pure functional core for server-side gate and
execution-lifecycle state. The live decider has three pure functions:

- **`Decide(ChangeSet, Signal) → []Event`** — all business logic. Maps an incoming
  signal against the prior state and emits a sequence of past-tense domain facts
  (e.g. `GateSatisfied`, `TargetRevoked`, `ExecutionFailed`). The event taxonomy
  covers every gate-lifecycle, execution-lifecycle, and claim-ledger transition.
- **`Evolve(ChangeSet, Event) → ChangeSet`** — the total fold. Applies a single
  event to the prior state, returning the new `ChangeSet`. Written replay-ready:
  replaying the event log over an empty state converges to the live snapshot.
- **`React(ChangeSet, []Event) → []Action`** — CQRS projection. Derives the
  idempotent `Action`s the imperative shell must run (grant requests, revokes,
  claim releases, `RenderCheckRun`, `PublishSSE`) from the folded state plus the
  event batch. Presentation (`RenderCheckRun`/`PublishSSE`) is never a stored fact.

The imperative shell (`internal/server/shell.go`) reconstructs the scoped
`ChangeSet` by replaying the event stream for a `(pr, environment)`, runs `Decide`,
folds the new events with `Evolve`, persists (appends events + snapshot + rebuilds
projections), then executes the `React` actions — serialized per `(pr, environment)`.
The event log is the source of truth; projections (e.g. `gate_targets`) are rebuilt
from the fold, never written directly. (Phase 3 split the pre-split `Step`
orchestrator into `Decide`/`Evolve`/`React`; `Step` was retired in the subsequent
docs-restructure/cleanup. See the PR series (Phases 1–3) for the full migration
reasoning.)

`GateState` is a proper sum type: `NotClassified` (PR never finalized — apply
fails closed), `Clean` (classified, zero gate targets — apply passes), `Pending`
(grants in flight), `Satisfied` (all grants `ACTIVE`), `Blocked`
(denied/expired/revoked or slot-collision). The leased requester SA lives inside
the gate variant — the clobber bug from `SetTargetRequester` is structurally
impossible. The per-stack `gated`/`safe` overlay is a **derived** projection of
`GateState`, never stored truth.

The reconcile core is the **sole** gate/execution engine: there is no legacy path
and no feature flag. (It originally shipped behind an off-by-default
`reconciler_core` flag; after cutover the flag, legacy handlers, and
`serve --check-quiescent` tooling were all removed.)

The decider tests (`internal/reconcile/decide_test.go`, `evolve_test.go`,
`react_test.go`) are the correctness oracle for all gate/execution/claim-ledger
transitions.

The apply-time gate check is **fail-closed on an unconfirmable reconcile**: if the
fresh PAM re-list cannot confirm current grant state (backend unreachable), the
check returns `503` and apply blocks. After a successful reconcile it reads the
verdict from the **replayed gate state** (the lossless `Decide`/`Evolve` fold of
the `exec:<pr>:<env>` stream), not the projection: `NotClassified`→`409`
not-classified, `Pending`/`Blocked`→`409` not-satisfied, `Clean`/`Satisfied`→`200`
(the `Satisfied` lease carries the requester). A `200` is only returned after a
successful fresh reconcile of a classified, satisfied (or clean) gate.

### `tfstackplan state` (Phase 6)

`tfstackplan state` is the operator-driven cross-stack state-move machinery.
**SP1 ships same-stack moves; SP2 adds cross-stack moves via native
import/removed; SP3 adds the faithful `terraform state mv` executor.** Verbs:

- `state move --dir DIR [--stack STACK] [--pr N] <from> <to> …` routes each
  declared `<from> <to>` pair by comparing the two sides' stacks. `--stack` is
  the default stack for unqualified addresses; an explicit `stack:addr` prefix
  overrides it. All pairs are validated against the relevant `tfplan.json`(s)
  before anything is written; ops accumulate per stack and the shim(s) are only
  written if every pair validates (fail-closed across both stacks). The matcher
  is prefix-based, covering resource / module / `count` / `for_each` addresses.
  - **Same-stack** (`from`/`to` in one stack): the destroyed `from` must have a
    same-type create under `to`, emitting a native `moved {}` block.
  - **Cross-stack** (`from`/`to` in different stacks): emits a native
    `import { to = <to> id = <id> }` block in the **destination** stack's shim
    (the `id` is read from the destroyed resource's `before.id` in the source
    plan) plus a `removed { from = <from> lifecycle { destroy = false } }` block
    in the **source** stack's shim — so the resource is adopted into the new
    state and dropped from the old without being destroyed.
    - With `--via mv`, the cross-stack pair is instead recorded as a
      `_tfsp_xmove.<key>.hcl` manifest in the **destination** stack
      (`source_stack` + the `from`/`to` intent pair verbatim — no fan-out to
      concrete per-resource addresses at generation time). A lightweight
      `CheckXMoveSource` validates that the source plan's `prior_state` is
      present and contains at least one resource under `from`; no destination
      plan is loaded. Fan-out to concrete addresses happens at apply time via
      `expandPairs` against live state. This keeps the manifest in the same
      address form as live state (pre-`moved{}` processing), preventing the
      plan/apply split that occurred when addresses were derived from
      `ResourceChanges`. See `docs/guide/08-state.md` § "Module extraction
      with `--via mv`" for the `moved { from = module.foo to = module.foo[0] }`
      pattern and its full workflow. See [PR #159](https://github.com/Fluent-Health/terraform-stack-plan/pull/159) for the root-cause analysis.

  Blocks are written to a PR-keyed shim `_tfsp_move.<key>.tf` in each affected
  stack dir; the normal `run apply` (backend-locked, no state surgery) applies
  them. Ops accumulate into the shim across invocations (existing blocks are
  merged, not clobbered). The key is `PR-<n>` from `--pr` / `$TFSTACKPLAN_PR`,
  else `branch-<name>` from the git branch, else `local`.
- `state list [--dir DIR] [--pr N]` lists discovered shims (`key`, stack, and a
  kind-aware op line: `moved from → to`, `import to (id=…)`, or `removed from`).
- `state cleanup --dir DIR (--pr N | --all | --applied)` removes shims. `--pr N`
  removes one PR's same-stack shims + xmove manifests; `--all` removes all; `--applied`
  removes **all xmove manifests only** (for post-apply cleanup after a cross-state move —
  same-stack shims are left intact since they are still needed for classification).
- `state check --dir DIR` reads the local `tfplan.json` files (written by `run plan`) and validates all pending xmove manifests without running terraform or mutating any files. For each manifest it reports: **spent** (move already applied — all To-addresses in dest `prior_state`), **valid** (source addresses found, no errors), **xmove/source-not-planned** (source stack has no plan and the move is not spent), or any `xmove/*` error codes. Exit 0 when all manifests are valid or spent. Useful for debugging without re-running the full classify pass.
- `state apply --dir DIR [--execute] [--lock]` discovers every
  `_tfsp_xmove.*.hcl` manifest and runs it via terraform-exec: pull both states
  → back up each (temp dir via `os.MkdirTemp`, path printed to stderr so it is
  findable for recovery but never inside the repo tree) → per-pair fail-closed
  decision table (source-only → **move**, dest-only → **skip** (idempotent),
  both/neither → **error**) → `terraform state mv -state/-state-out` against the
  pulled local files → push both, **never** `--force`. **Dry-run by default**
  (prints "would move" / "skip"); `--execute` performs the moves. Requires
  `terraform` on `PATH`. The discover→execute→print core is the package-level
  `applyPendingMoves`, shared with the `run apply` pre-phase (below).
- **Unified fail-closed validation.** Cross-state moves (`_tfsp_xmove.*.hcl`) are validated by a single pure validator `ValidateMovePlan` in `internal/statemove`. The canonical address namespace across all three stages is `prior_state` (the pre-`moved{}` snapshot embedded in plan JSON), which equals live state at xmove time because xmove runs as a pre-phase before source apply. **Data sources (`mode=data`) are filtered out of all address walks** — wildcards never sweep `data.*` into the move set, since data sources are re-read at the destination and cannot be `state mv`'d. The destination-provider check (`xmove/provider-missing`) treats a set of always-satisfiable providers as present without a declared config: the config-less HashiCorp utility providers (`random`, `null`, `local`, `tls`, `archive`, `template`, `time`) and the **built-in `terraform` provider** (`terraform.io/builtin/terraform`, backing `terraform_data`/`terraform_remote_state`), which cannot be declared in `required_providers` or a `provider` block and so would otherwise always fail the check.
  - *Generation-time:* `--via mv` calls `CheckXMoveSource` — hard error if `prior_state` is absent or contains nothing under `from`. No ResourceChanges fallback.
  - *Plan-time enforcement:* `validateXMoveManifest` in the classify pass reads only `prior_state` for source addresses — hard error if absent (no ResourceChanges fallback). It then runs `ValidateMovePlan(isApply=false)` against those addresses and the destination plan's provider config, failing exit 1 on any `error`-severity diagnostic (including `xmove/provider-mismatch` when source and destination use different providers). **Manifest lifecycle:** when the source plan file is absent entirely, the classify pass checks the destination's `prior_state`; if all declared To-addresses are already present there, the manifest is **spent** and emits the info-only `xmove/spent` diagnostic (green — the move already applied), otherwise it emits `xmove/source-not-planned` (red) with a fix hint. Spent detection is also **per-entry inside `ValidateMovePlan`** ([#181](https://github.com/Fluent-Health/terraform-stack-plan/pull/181)): when the source plan *is* present but an entry's From address is gone from its `prior_state` *and* the destination `prior_state` holds the To address, the entry is spent (already applied by an earlier PR) and emits `xmove/spent` (warning severity, printed as info) instead of the hard `xmove/source-missing` — so a spent manifest still on `main` awaiting its GC PR doesn't fail unrelated PRs' `run plan`. `xmove/source-missing` still fires when the source is gone and the destination does **not** hold the target (genuinely stale/mis-keyed). This means follow-up PRs after a cross-state move plan green whether or not they touch the source stack. **Data-source orphan warning:** `xmove/data-source-orphan` (warning, non-fatal) is emitted for each data source that falls under the From prefix in source `prior_state` — these are filtered out of the move set and will remain in the source stack. The operator must `terraform state rm` them before the source stack can be retired.
  - *Apply-time pre-flight:* `ValidateMovePlan(isApply=true)` runs against live pulled state addresses as the final fail-closed guard before any state surgery; `expandPairs` fans intent pairs out against live state at this point.
- **Dest-push-failure rollback.** If a move's dest `StatePush` fails after the
  source push already succeeded (resources removed from the source's live state
  but not yet in the dest's), `Execute` **rolls the source back** to its
  pre-move state — re-pushing the in-memory pre-move state with a recovery-only
  `--force` (the forward pushes stay `Force(false)`), on a non-cancellable
  context so an aborted request still recovers. If even the rollback push fails,
  the error is loud and points at the temp backup dir (printed to stderr at
  apply start) for manual restore. (Previously the moved resources were left
  lost from both states and the backups were never restored.)
- **`run apply` cross-state move pre-phase.** After the gate check and before
  terramate runs, `run apply` executes any pending `_tfsp_xmove.*.hcl` manifests
  (via the same `applyPendingMoves`, always `--execute`). It is **fail-closed**:
  a manifest that cannot land cleanly aborts the apply (exit 1) so terramate
  never plans against a stale/half-moved state. It is a **no-op** when no
  manifests exist (the common case), so this is transparent to ordinary applies.
  `--state-lock` opts into the pessimistic GCS lock for the moves.
- **Concurrency.** Without `--lock`, safety rests on terraform's built-in
  serial/lineage check on `state push` (an optimistic check) plus the backups.
  `--lock` (with `--execute`; `--state-lock` on `run apply`) adds a
  **pessimistic** lock: before each move it
  acquires the GCS `.tflock` object (`<prefix>/default.tflock`) — the same object
  terraform's GCS backend uses — via an `ifGenerationMatch=0` upload, so a
  concurrent terraform op fails to lock and the move **fails before** touching
  state (rather than mid-flight). It is **fail-fast**: an already-held lock errors
  out instead of waiting. The `statemove.Locker` is pluggable; `cmd/tfstackplan`
  supplies a dependency-free GCS implementation (`gcslock.go`) wired from ADC
  (`gcpCreds`). The backend bucket/prefix are read from the stack's `*.tf`
  (`terraform { backend "gcs" { bucket prefix } }`).

- `state moves-manifest --dir DIR [--pr N] [-o FILE]` discovers every
  `_tfsp_move.*.tf` shim and `_tfsp_xmove.*.hcl` manifest under `--dir` and
  emits a **two-sided** `--state-moves` JSON (`{"<stack>":["<addr>",…]}`):
  - Source move-outs (shim `removed.From`, xmove `Pair.From` under `SourceStack`)
    — planned as destroys on the source stack.
  - Destination move-ins (shim `import.To`, xmove `Pair.To` under `DestStack`)
    — planned as creates on the destination stack.
  - Same-stack moves (shim `moved.To`) — defensive inclusion.
  Addresses within each stack are sorted and de-duped; stacks with an empty set
  are omitted. The JSON shape is exactly what `statemoves.Load` (used by
  `render/classify --state-moves`) consumes, so feeding this file to the
  classifier neutralizes the spurious IAM gate that fires on the source stack's
  planned destroys. CI wiring: `state moves-manifest --dir . -o moves.json` →
  `render/classify --state-moves moves.json`. `--pr N` limits output to one PR's
  moves; `-o FILE` writes to a file instead of stdout. Addresses are the concrete
  ones recorded at `state move` time; if the live plan instances drift, `Covers`
  may not expand siblings — conservative (gate re-flags rather than wrongly
  suppresses). See [project-management#4195](https://github.com/Fluent-Health/project-management/issues/4195).

The projecting side already classifies cross-state move-targets as relocations
via `--state-moves`. With `state moves-manifest`, the `--state-moves` JSON is
now two-sided (source move-outs AND dest move-ins), produced entirely from the
project's own move declarations.

State-move discovery is **fail-closed**: a file in the reserved `_tfsp_move.*` /
`_tfsp_xmove.*` namespace that cannot be parsed errors the read path (the
classify pass, `state apply`, `state list`, `moves-manifest`) rather than being
silently skipped — a silently dropped manifest would let a relocation classify
as (and apply as) a real destroy+create. The filename is the authoritative key:
the `# tfstackplan:key=` header comment is optional and used only as a
consistency check when present (mismatch → error); hand-authored manifests
without the header are accepted and keyed by filename. `state cleanup` matches
by filename and does not parse, so a corrupt or key-mismatched file is always
removable.

### Apply serialization — merge-lock

`tfstackplan serve` prevents overlapping tier-applies from colliding on per-stack
Terraform state locks. It is always on (no flag): serve holds a PR's merge while
its changed stacks overlap an in-flight apply. On an **unarmed** (legacy) tier
this is a standalone required check, `apply-lock/<env>`; on an **armed**
(serve-as-driver) tier the verdict is folded into the consolidated
`terraform/<env>` check instead — see *Consolidated `terraform/<env>` check*
below for the merged surface and its precedence.

**Mechanism.** The claim ledger is event-sourced on the `env:<env>` stream (Phase
4c): the `ClaimSet` is the folded state of claim/renew/release events, loaded via
the generic `internal/eventsourcing` host. `apply_claims` is a **derived
projection** — a cross-env SQL index written by the projector for the sweep + UI;
it is not read for the overlap verdict. `held` is a **read-time projection**:
expiry (`ExpiresAt > now`) is enforced at query time, not as a logged event.

When serve evaluates whether a PR is safe to merge it replays the `env:<env>`
stream to get the current `ClaimSet`, then computes the *overlap* between the PR's
plan-time changed stacks and the live claimed set. An empty intersection → `clear`;
any overlap → `held`. If the PR's plan stacks cannot be determined (no finalized
plan, or the store is unreadable) the verdict is `unverifiable` — the subsystem
**fails closed**. On an unarmed tier this verdict drives a standalone required
GitHub check named `apply-lock/<env>`:

- `clear` → check conclusion `success`.
- `held` or `unverifiable` → check conclusion `pending` (blocking the merge).

On an armed tier there is no standalone check on a PR head — the same verdict is
computed at every terminal render of the plan execution and folded into the
consolidated `terraform/<env>` check (below).

**Two webhook front-ends** over the same overlap predicate; infra's governance
ruleset chooses which to enforce:

- **`merge_group`** (GitHub merge queue) — evaluated on the `merge_group.checks_requested`
  event. The PR is already isolated in a merge-queue branch, so the claim is
  written atomically at the moment serve posts the `success` check (greenlight).
  This is race-free: the merge-queue serializes PRs into the queue one at a
  time, so two PRs with overlapping stacks cannot both receive `success`
  simultaneously. The check posted on the group head is lock-only (there is no
  plan execution against a merge-group SHA); named `apply-lock/<env>` on
  unarmed tiers, `terraform/<env>` on armed ones.
- **`pull_request`** — on an unarmed tier the check is posted on the PR's head
  SHA. Because the `pull_request` webhook (open/sync) fires *before* the plan
  registers the PR's changed stacks, the check is **also (re-)posted when the
  plan finalizes** (`handleFinalize` → `postPlanApplyLock`, on the same SHA as
  `plan/<env>`) — so on a freshly-opened PR it appears reliably alongside
  `plan/<env>` rather than only after a later push. On an armed tier this
  separate posting is skipped entirely: the lock verdict is computed inline by
  the plan execution's own terminal render (`renderAndPatch`), so there is
  nothing to (re-)post standalone. Either way the claim is written at the
  `push` event that delivers the merge commit (i.e. on actual merge, not at
  greenlight). Simpler to deploy
  (no merge queue needed) but has a residual race: two PRs with overlapping
  stacks that receive `clear` within the same window can both merge before either
  claim is recorded.

**Claim lifecycle and auto-heal.** Claims are associated to the apply execution
at `Init` time and kept alive by a **heartbeat lease** — each apply tick from
`run step` renews the claim's expiry. The claim is released **when the apply
truly ends**, not on a `Finalize`: `run apply` emits a mid-run `Finalize` during
its classify pass (re-classify + re-request grants, *before* the cross-state
move pre-phase and the terramate apply), so releasing on `Finalize` dropped the
lock before the apply had started. Instead, release is a reconciler-core
transition: the runner's apply-end `GateRevoke` (sent only at `run apply`'s
terminal step, never during the classify pass) maps to `reconcile.ApplySucceeded`,
and `stepApplySucceeded` emits a `ReleaseClaim` action (alongside the grant
revokes) that the shell executes — release the claim *and* the grant the
finished apply held, in one transition. A failed apply still sends `GateRevoke`,
so it releases too; a classify-fail abort (no `GateRevoke`, grant kept for
retry) leaves the claim to the TTL sweep — the safe over-hold direction,
identical to how the grant behaves. (See the `ApplySucceeded` cases in the decider tests
(`internal/reconcile/decide_test.go`, `evolve_test.go`, `react_test.go`) and
PR #100 for the full root-cause + alternatives.)

A `ClaimsSweepLoop` (periodic background task) releases claims whose lease has
expired — a signal that the apply process died without calling finalize — and
re-evaluates all `pending` `apply-lock/<env>` checks for the same environment,
so a stuck check self-heals once the dead apply's TTL lapses.

> Acquisition (merge / merge_group) and the cross-PR overlap/held evaluation +
> check posting stay in the imperative shell — they are env-global, not per-(PR,
> environment), so they don't fit the reconciler's per-`(pr, environment)`
> scope. Only the per-(PR, environment) terminal release lives in the core.

**Admin un-wedge.** `tfstackplan claims list [--env ENV]` shows active claims
(PR, stacks, expiry); `tfstackplan claims release <pr> [--env ENV]` forcibly
releases a stuck claim. A GitHub repository admin can also post a `success`
bypass directly via the GitHub Checks API, skipping the predicate entirely.

**Known limitations / gotchas (apply serialization):**

- **Race-freedom depends on infra's chosen enforcement.** The `merge_group`
  front-end is race-free (the merge queue serializes PRs and the claim is
  recorded at greenlight). The `pull_request`+auto-merge front-end has a
  residual race: two PRs with overlapping stacks can both see `clear` and
  auto-merge before either claim is recorded. Use the merge queue
  (`merge_group`) when strict serialization is required.
- **Merge-queue head-of-line blocking.** A long-running apply holds its claim
  for the full apply duration. A second PR with any overlapping stack queues
  behind it and cannot enter the merge queue (its `apply-lock/<env>` check stays
  `pending`). The effective throughput is one apply per overlapping-stack-group
  at a time.
- **`merge_group` → PR resolution assumes group size 1.** Serve resolves the
  merge-group's head SHA back to a PR by matching the group's branch name
  (`refs/heads/gh-readonly-queue/<base>/pr-<n>-...`). This assumption holds
  for a solo-entry merge queue (the default) but breaks if multiple PRs are
  batched into one merge-queue entry.
- **The lease window must exceed the apply heartbeat gap.** If the claim's TTL
  is shorter than the interval between `run step` ticks, the `ClaimsSweepLoop`
  can mistakenly release a live apply's claim and unblock a racing PR. The
  configured TTL should be at least 2× the longest expected tick gap.
- **The overlap predicate uses plan-time stacks.** The claimed set is populated
  from the stacks recorded at plan `Finalize`. If the base branch moved (e.g.
  another PR merged a new stack) between the plan and the apply, the predicate
  may use a stale stack set. When strict required checks are enabled, the merge
  queue forces a re-plan in that case, refreshing the set.
- **Release is driven by the `ApplySucceeded` reconcile transition.** The apply-end
  `GateRevoke` drives `reconcile.Step`'s `ApplySucceeded`, which emits a
  `ReleaseClaim` action — the shell drops the PR's claims and re-evaluates the
  env's held checks. A mid-run classify-pass `Finalize` deliberately does NOT
  release (it would drop the lock before the apply ran); the claim lease TTL is
  the crash backstop.

### Consolidated `terraform/<env>` check (armed tiers)

On an **armed** tier (`runTriggerArmed()` — an `executor "<backend>" {}` block
configured and `server { environment }` set, i.e. serve driving CI runs itself)
a single `terraform/<env>` check per PR head replaces the unarmed tier's
two-check surface (`plan/<env>` + `apply-lock/<env>`). It is a **pure render**
of execution state × gate state × merge-lock state, recomputed on every
terminal render of the plan execution (`renderAndPatch`) — there is no
independent posting path to fall out of sync. Precedence (`conclusion()` +
the lock fold in `renderAndPatch`):

1. Any stack failed → `failure`.
2. A rejected PAM approval gate (denied/revoked/expired) → `action_required`;
   a gate awaiting its human keeps the check `in_progress` (`gatesSection` names
   which gate; unchanged from the unarmed surface).
3. Still planning/applying → left `in_progress`.
4. The merge-lock is held (or unverifiable) and would otherwise be the sole
   blocker → left `in_progress`, title `"waiting on PR #N's apply"`
   (`lockTitle`) so a reviewer doesn't read a stalled bar as stuck. This
   clears **automatically**: the blocking apply's release re-evaluates and
   re-drives this same check (`reevaluateHeld` → `drive`) — no re-push needed.
5. Otherwise → `success`.

**Merge-group heads.** A GitHub merge-queue group head has no plan execution
against its SHA — there is nothing to fail or gate — so `handleMergeGroup`
posts a **lock-only** check there. It is named `terraform/<env>` when armed
(`apply-lock/<env>` on unarmed tiers) via the shared `mergeGateCheckName`,
so the required-check name is identical whether the head is a PR or a merge
group.

**Post-merge `apply/<env>` is unchanged** in both modes — the consolidation is
scoped to the pre-merge PR-head surface.

**Unarmed (legacy) tiers** keep the two-check surface until each tier's infra
cutover (widening webhook events, arming the `executor` block, wiring
`_EXECUTION_ID`). The legacy posting paths (`postApplyLock`,
`postPlanApplyLock`, the standalone `apply-lock/<env>` check) stay in the
codebase, gated behind `!runTriggerArmed()`, and are only removable after
every tier has cut over.

**Known limitations / gotchas (consolidated check):**

- **The stored gate identity never changes.** `executions.status_context`
  stays `plan/<env>` regardless of which name the check run wears — `isGate`,
  supersede bucketing, and pre-cutover rows all key off `status_context`, not
  the display name. Only the GitHub-facing name (`planCheckName` /
  `consolidatedCheckName`) flips with arming.
- **The Re-run matcher accepts both names.** `handleCheckRunWebhook` matches
  `check_run.name` against both `planCheckName(env)` (the live name) and the
  legacy `checkRunName(env)` (`plan/<env>`), so a check posted before a
  mid-flight cutover keeps a working Re-run button after arming.
- **A Re-run on a merge-group head's `terraform/<env>` check degrades safely
  to a logged skip.** There is no execution row keyed to a merge-group SHA, so
  `store.LatestPRForSHA` finds no PR and the handler logs and returns
  no-content — no crash, no misattributed re-run.
- **The held-lock record points at the owning plan execution.** In consolidated
  mode, `applylock_checks.execution_id` carries the id of the plan execution
  whose check render carries the lock verdict. A claim release
  (`reevaluateHeld`) re-drives that execution's check directly rather than
  patching the lock check standalone — there is no standalone lock check to
  patch once armed.

### API auth — Google OIDC identity + scopes

`/api/*` auth has moved from one shared symmetric secret to **verified caller
identity** (increment 1 of the CI/CD-driver evolution). Historically every
caller — CI runner, human CLI, agent — locally minted an HS256 JWT
(`sub="runner"`, `aud="api"`, 1 h) from the tier's `tfstackplan-token` secret:
no real identity to audit, possession = full power, and any leak was a
full-API credential until a global rotation. That shared-secret path is now
**fully deleted** from `/api/*` — `App.auth` no longer branches on the JOSE
header `alg` at all; OIDC is the only verified path. `internal/jwtutil` itself
is gone too: its last job was the 30-day view-JWT, which retired with the HTML
viewer (the central UI has its own Google-login sessions), taking
`serve { webhook_secret_env }` with it.

The replacement is **Google-signed OIDC ID tokens** — the same mechanism
already verifying Pub/Sub pushes:

- **Server**: the `serve { api_auth {} }` block declares the accepted token
  audiences and an identity → scope allowlist (`principal "<email>" { scopes }`).
  The injectable `App.APIVerifier` (shape-identical to `PushVerifier`;
  `idtoken.Validate` under the hood) verifies signature + audience and returns
  the email; the `auth` middleware maps it to scopes — `report` (execution
  lifecycle, logs, gates, claims), `read` (execution/claims reads), `admin`
  (claim release, future admin verbs) — each route listing the scopes that may
  call it (any-of). The verified actor rides the request context (`Actor(r)`)
  for audit use by the planned admin unstuck verbs.
- **Audiences**: service-account callers mint tokens for the serve URL.
  User-ADC callers can't mint custom audiences — their tokens carry the fixed
  gcloud client id, accepted via `extra_audiences`. The audience check runs in
  the verifier against this allowlist (not inside `idtoken.Validate`).
- **Clients**: `internal/gauth` obtains ID tokens from Application Default
  Credentials, in order: `idtoken.NewTokenSource` (SA keys, GCE/GKE metadata,
  impersonated creds); then — when a metadata server exists but its
  identity endpoint doesn't (**Cloud Build**, unlike real GCE, does not
  implement it, discovered live during the runner flip) — the IAM Credentials
  `generateIdToken` API as the ambient SA itself, which requires the SA to
  hold `roles/iam.serviceAccountOpenIdTokenCreator` on itself (self-grant in
  the infra companion); finally the `id_token` riding the user-ADC refresh
  grant. OIDC is **opt-in via `TFSTACKPLAN_AUDIENCE`**
  a token-less environment never probes
  ambient machine credentials, never hard-fails on a stale ADC file, and never
  sends a replayable ID token to whatever host the server URL happens to name.
  The CI flip is config-only: swap the injected token env for the audience
  env, and builds authenticate as their build SA. Token minting is bounded by
  the client's 10 s timeout (`gauth` honors the caller's context), and
  credential discovery — including `idtoken.NewTokenSource`'s eager
  construction-time fetch — is bounded separately by `gauth.SourceTimeout`
  (built on a background context deliberately: oauth2 binds the construction
  context into future refreshes), so a hung metadata server cannot stall a
  best-effort tick at either stage.
- **HS256 deleted from `/api/*` (post-migration)**: every caller flipped to
  OIDC, so the dual-accept migration posture was removed — an HS256 token
  minted from any secret (right or wrong) now gets a
  flat 401 on every `/api/*` route once `api_auth {}` (`App.APIVerifier`) is
  configured. `runner.NewClient` dropped its secret parameter entirely
  (`NewClient(baseURL)` is unauthenticated; `NewClientTokenSource(baseURL,
  tokenFunc)` is the OIDC path), `runner.EnvToken` and `jwtutil.Alg` are gone,
  and `cmd/tfstackplan run status`'s `--token` flag was dropped (OIDC via
  `$TFSTACKPLAN_AUDIENCE` only). The shared secret's only remaining job is the
  30-day view-JWT; it is expected to be deleted together with that
  machinery once the planned central UI replaces the viewer.
  Claim release is deliberately not ownership-checked (the runner releases
  claims for whichever PR it applies — an association the server cannot
  verify); the verified actor is what gets audited.
- Auth is disabled only when `api_auth {}` (`App.APIVerifier`) is not
  configured (local/dev), preserving the old escape hatch — `WebhookSecret`
  no longer has any bearing on this.
- **Offline e2e via a fake Google issuer**: `internal/gauth` owns both halves
  — `Source`/`SourceTimeout` (client minting) and `Verifier` (server
  verification: signature/expiry via `idtoken`, audience allowlist, email +
  `email_verified`, injectable key-fetch client) — and `gauth/gauthtest`
  fakes Google entirely: a throwaway RSA key signs ID tokens, the validator
  fetches the matching JWKS through an injected HTTP client (`idtoken`
  verifies signature/expiry/audience but does not pin the issuer), and a
  fabricated service-account key whose `token_uri` points at a local
  JWT-bearer endpoint drives the *real* client-library minting path. The e2e
  suite runs the whole loop — SA key → mint → bearer → verify → scope
  enforcement — with real cryptography and zero credentials or network, so
  fork PRs and OSS contributors run it too. A real test user/OAuth setup was
  deliberately rejected (public repo, secretless fork CI, ToS-fragile robot
  accounts); real-Google integration is what the nonprod rollout bake covers.

### The `/api` OpenAPI contract

Everything under `/api` is described by a hand-written OpenAPI 3 document,
**`api/openapi.yaml`** — the wire contract for the runner→server protocol
(init → phase → update → finalize, log ingest, the gate exchange), the claim
verbs, and the execution read. `oapi-codegen` (a `go.mod` tool; regenerate with
`go generate ./internal/api`, CI fails when out of sync) generates three things
from it into `internal/api`:

- the **std-http router** (`HandlerFromMux` onto the same Go 1.22 `ServeMux`)
  — serve mounts it through a thin `apiServer` adapter whose methods delegate
  to the existing handlers, so the handler bodies (and the wire bytes) are
  untouched;
- the **models**, bound to `internal/events` via `x-go-type` — the events
  package remains the single canonical protocol definition, no duplicate
  type set;
- the **typed Go client**, which `internal/runner.Client` now rides (bearer
  minting is a `RequestEditorFn`; the runner's conventions — disabled on empty
  URL, best-effort non-2xx-to-error collapsing, the fail-closed gate check
  reading status + coded body raw — are preserved on top).

Authorization lives in the contract too: each operation's accepted scopes are
its `security` requirement; the generated wrapper injects them into the request
context and one `apiAuth` middleware enforces them — the spec is the single
source of truth for who may call what.

**The contract is cross-version.** Serve and runner deploy independently, so a
spec change is a protocol change: additive only, never a rename/removal. The
byte-level truth is pinned by `internal/server/testdata/wire/` — golden
snapshots (status, Content-Type, exact body) replayed by
`TestAPIWireCompat`; regenerate with `WIRE_GOLDEN=update` only for a
deliberate, reviewed wire change. Preserved historical accidents the goldens
enshrine: `GET /api/execution/{id}` emits PascalCase keys with a raw
`sql.NullInt64` `CheckRunID`, and `graph.edges` may be `null`.

Deliberately outside the contract: the SSE streams (`/api/execution/{id}/events`,
`/logs/...?follow=1` — OpenAPI models event streams poorly; they stay
hand-rolled), the GitHub webhook and Pub/Sub push endpoints (external
contracts), and the public `/img`/`/assets`/`/live` surfaces. The spec is the
foundation the planned central UI, CLI verbs, and MCP tooling generate their
clients (and TypeScript types) from.

**Read endpoints for aggregating consumers** (added for the central UI; clean
snake_case shapes, unlike the frozen legacy execution read):

- `GET /api/executions` — recent execution summaries, newest first (`?limit=`,
  default 100, cap 1000); `?pr=` narrows to one PR's full timeline across
  plan/apply/verify runs. Summaries exclude the rendered report.
- `GET /api/approvals` — every gate target not yet ACTIVE across all PRs (the
  `PendingGates` predicate, so DENIED/REVOKED surface too — they need
  attention even though the action is not "approve"), each with the PR context
  (repo from the PR's latest execution in that environment) and the grant
  resource name a future in-UI approve call targets.
- `GET /api/execution/{id}` additionally carries `verify_execution_id` — the
  latest verify execution for the same (pr, environment) — so the validation
  view needs no HTML-only plumbing.

### The central UI face (`tfstackplan ui`)

The single pane of glass over both tier serves: a **stateless aggregator** —
no domain state of its own — deployed as its own service and configured by the
top-level `ui {}` block (`tier "<name>" { url }` per tier serve, `oauth {}`,
`session_secret_env`, `public_base_url`; reference in
`examples/ui.tfstackplan.hcl`).

- **Human auth — in-app Google OAuth** (Workspace-internal client, no IAP/LB):
  authorization-code flow with `openid email profile`; the Workspace domain is
  enforced against the **verified** id_token's `hd` claim (a consumer account
  carries none → 403), `email_verified` required. The session is an
  **AES-256-GCM encrypted cookie** (stdlib crypto; key = SHA-256 of the
  configured secret, so rotating the secret invalidates every session) holding
  identity only — the SPA never sees Google tokens and the backend stores
  none. `?next=` is open-redirect-safe (same-origin absolute paths only).
  Everything except `/healthz` and the SPA shell sits behind the session.
- **Service auth toward the tiers — Google OIDC** (`gauth.Source` per tier
  audience, default the tier URL): the UI's service account needs a
  `read`-scoped principal in each tier's `serve { api_auth {} }`. A
  credential-less environment (local dev) degrades to unauthenticated tier
  calls rather than failing startup — an auth-requiring tier then rejects them
  visibly.
- **Contract-first JSON API** — `api/ui.openapi.yaml` → `internal/uiapi`
  (generated router; the SPA's TypeScript types come from the same document):
  `/api/me`, `/api/tiers`, and tier-scoped proxies
  (`/api/tiers/{tier}/executions[?pr,limit]`, `/api/tiers/{tier}/approvals`,
  `/api/tiers/{tier}/executions/{id}`). Proxied schemas are `x-go-type`-bound
  to `internal/api`, so the UI contract re-exposes the tier contract's shapes
  and the two cannot drift. Proxies build **typed requests via the generated
  tier client but relay response bodies verbatim** (no decode/re-encode
  drift); a dead tier is a `502` naming the tier and never affects the others;
  an unknown tier is `404`. The OAuth browser flow (`/auth/*`) is deliberately
  outside the contract.
- **GitHub App webhook relay** (`POST /github/webhook`): the single ingress
  for App-scoped webhook deliveries (the Re-run buttons' `rerequested`
  events), forwarded **verbatim** — signature headers included — to every
  tier's `/github/webhook`; each serve verifies GitHub's HMAC itself
  (end-to-end authenticity). `github_webhook_secret_env` optionally makes
  the relay verify too (defense in depth: garbage dies here, visible in the
  App's delivery log). 202 when any tier accepted; 502 when none did, for
  manual redelivery.
- **Streaming proxies** (session-authed, outside the contract like their
  tier-side counterparts): `/api/tiers/{tier}/executions/{id}/events` relays
  the tier's SSE change stream, `/api/tiers/{tier}/logs/{exec}/{stack...}`
  relays log reads including `?follow=1` with `Last-Event-ID` resume, and
  `/api/tiers/{tier}/plan/...` relays the rendered plan HTML fragment. The
  stream client has no overall timeout (lifetime binds to the browser's
  request context), relays flush per read, and headers are flushed
  immediately — an idle SSE stream must still look connected to
  `EventSource`.
- **The SPA** (`web/ui/`: SolidJS + TypeScript + Vite + Tailwind/daisyUI) is
  **PR-centric** — the unit of work is a PR, which *contains* its tiers
  (`nonprod`/`prod` = the data `environment`), not the reverse. Three surfaces
  behind a persistent nav rail: **PRs** landing (`/`: active PRs newest-first,
  each with a per-tier worst-of-live status dot; resilient to N tiers),
  **Ops board** (`/ops`: the debug surface — an **applier-slot panel** per tier
  (capacity `used/total`, one card per pool slot showing the occupying PR ·
  grant · elapsed or a dashed free slot, and waiting-for-slot PRs under a
  saturated pool — from the tier's `GET /api/inspect/pool`, proxied through the
  central-UI backend), errored runs, and awaiting-approval; each per-tier fetch
  isolated so a dead tier degrades only its own panel), and the **PR view**
  hero (`/pr/{n}`): both tiers side-by-side, each showing its *newest* execution
  as primary — per-stack **progress blocks** (one block per changed stack,
  coloured by status, parallel-aware, failures visible) + **context chips** for
  the other `plan`/`apply`/`verify`/gate executions — with changes grouped by
  **project** (the stack grouping key *and* PAM gate target), an errored stack's
  `detail` shown inline, and an inline Plan/Log drill-in. Approvals are not a
  separate page — they surface on the Ops board and, in context, on the PR the
  gate belongs to. Visual language is a neutral "calm control panel": a custom
  daisyUI theme with a system/light/dark switcher, one accent, a fixed semantic
  status set (dot + word, not loud pills), monospace reserved for identifiers;
  ships no proprietary brand assets (public repo). Live updates are
  **reload-free by construction**: the payload-less `changed` stream debounces
  into a JSON refetch and Solid patches only what moved (list rows keyed by
  position via `<Index>` so an open drill-in/live-log survives refetch);
  `superseded` re-picks the tier's current execution in place; logs append via a
  TS port of term.js's ANSI/CR line buffer (all bytes escaped; vitest-covered).
  Plan diffs stay server-rendered — the SPA injects the tier's fragment and
  never re-implements the renderer. Payload types are generated from the tier
  contract (`yarn gen:types` → `src/api/tier-schema.d.ts`, committed, CI-checked).
  The PR view also carries a **PR identity header** (title/description/author/
  branch/↗GitHub) and a **merge strip** (automerge + per-tier `terraform/<tier>`
  check status + a "what's blocking merge" line; no queue position), and
  **in-context approve/deny** on gated projects (reusing the PAM flow). These are
  fed by serve persisting PR metadata from the `pull_request` webhook (`pr_meta`
  table) and a per-tier read `GET /api/pr/{n}` (identity + merge state), additive
  and wire-neutral — an older serve simply lacks the route and the SPA degrades to
  the minimal `#{n}` header. Still deferred to follow-on work: richer per-stage
  progress detail (the tick-segment lifecycle stepper) and the per-tier execution
  causality DAG on the PR view (each needs further serve-side additions).
- **SPA delivery**: the binary embeds `internal/ui/dist/` (`go:embed`), served
  with an index.html fallback for client-side routes. The repo commits only a
  placeholder page; the release workflow builds the SPA once and overwrites
  `dist/` before each `go build` (CI runs typecheck/vitest/build on every PR)
  — the same committed-asset contract as the serve CSS, keeping `go build`
  node-free. Local dev: `yarn dev` proxies `/api`+`/auth` to a running
  `tfstackplan ui` (see `web/ui/README.md`).
- `gauth` grew `ClaimsVerifier` — `Verifier`'s claims-returning core — for the
  `hd` check; `Verifier` is now a thin email/verified wrapper over it.
- **In-UI PAM approve/deny — the incremental-consent popup.** Stateless by
  construction: `GET /auth/approve` seals the intent (session email, tier,
  grant name, decision, reason, 2-minute expiry) into the OAuth `state`
  parameter with the session AEAD and redirects to Google requesting
  `cloud-platform` (PAM exposes no narrower scope; the token is still bounded
  by the user's own IAM) with `include_granted_scopes` — one consent per
  user, silent popups afterwards. The callback validates the sealed intent
  against the live session, exchanges the code, spends the user's short-lived
  access token on the single PAM `:approve`/`:deny` call
  (`gcppam.DecideGrant`) and discards it, then reports to the opener via
  `postMessage`. No user credential is ever stored; the server holds no
  approver capability of its own — GCP enforces the human's IAM (a PAM 403
  travels verbatim to the popup) and the PAM audit log records the human.
  `oauth { quota_project }` names the project user-token API quota attributes
  to (the OAuth client's project, which must have the PAM API enabled). The
  OAuth client needs `<public_base_url>/auth/approve/callback` as a second
  authorized redirect URI.

Still to come (tracked increments): a Playwright smoke, group-DAG/triage
parity in the execution view, and the tier serves' HTML viewer retirement
(check-run links then point here).

### Serve as the CI driver — webhook-triggered runs (inert until configured)

The second increment of the CI/CD-driver evolution: serve receives the GitHub
webhooks and starts the builds itself, instead of Cloud Build's own GitHub-app
event triggers. Feedback (check run + live link) appears within the webhook
turnaround — before any build machine spins up. Everything is **inert unless
both** an `executor "<backend>" {}` block is configured **and** the tier
environment is known (`server { environment }`); a disarmed serve behaves
exactly as before.

- **Event-sourced run lifecycle**: webhook → `RunRequested{kind, sha, branch,
  rerun}` through the shell → `RunQueued` (execution row + "queued" check run
  materialized, `StartRun` issued) → `RunStarted{buildRef}` or
  `RunStartFailed{reason}` (terminal check failure — a build that never starts
  is no longer silent). A new SHA supersedes a live plan run (`RunSuperseded` →
  store supersede + best-effort build cancel); a live apply is never disturbed.
  Same-(kind,sha) redeliveries no-op in the decider. Execution ids are minted
  deterministically (`run-<pr>-<env>-<kind>-<sha12>-a<attempt>`) — the pure
  core cannot use randomness — and the build must report under that id (the
  `_EXECUTION_ID` substitution → `TFSTACKPLAN_EXECUTION`, wired at cutover).
- **Ingest**: `pull_request` opened/reopened/synchronize → plan run for the
  head SHA; `push` to main → apply run, PR recovered from the merge-commit
  subject (squash/merge conventions; direct pushes are skipped); `check_run`
  rerequested (GitHub's per-check Re-run button) → the same kind again for
  THIS tier's check names only, bumping the attempt; `check_suite`
  rerequested (the suite-level "Re-run failed checks" button) → re-runs each
  kind whose latest execution at that (env, sha) concluded failure — green or
  pending runs are left alone.

  **Re-run delivery (learned the hard way)**: GitHub delivers
  `check_run.rerequested` / `check_suite.rerequested` ONLY to the GitHub App
  that owns the check — repository webhooks get just `created`/`completed`.
  So the Re-run buttons reach serve via the **App webhook**, pointed at the
  central UI's `/github/webhook` relay (one App URL, two tier serves). The
  relay is a **verbatim pipe**: signature headers travel through and every
  serve verifies GitHub's HMAC itself, so authenticity stays end-to-end — a
  compromised relay cannot forge events. Operationally the App webhook and
  the repo webhooks share one secret value (GitHub-held either way); a
  single-tier deployment skips the relay and points the App webhook straight
  at its serve. The repo webhook keeps delivering `pull_request`/`push`
  straight to each serve — the critical CI-driving path never routes through
  the aggregator. The native Cloud Build check's Re-run
  does nothing post-cutover (Google's app has no event-triggered build to
  re-run for our API-invoked manual builds) — operators use OUR checks'
  Re-run; the native check is informational.
- **Executor seam** (`internal/executor`): `Backend{Start, Cancel, Probe}`.
  Only `cloudbuild` is implemented — gcppam-style injected token func + raw
  REST (`triggers.run` with `commitSha` + substitutions, `builds.cancel`,
  `builds.get`), offline-tested against a fake endpoint. The Cloud Build
  trigger definitions stay terraform-managed; serve runs them by name. Other
  backends (github-actions `workflow_dispatch`, gitlab, generic webhook) are
  documented shapes behind the same seam.
- **Shell integration**: the fixpoint loop generalized from grant observations
  to feedback signals — `StartRun` yields `RunStartResult` exactly like
  `RequestGrant` yields `GrantsObserved`. `project()` marks start-failed runs'
  execution rows failed (nothing else would ever move them off in_progress).
- **Start watchdog** (`RunWatchdogLoop`, 1 min cadence, 10 min timeout): an
  execution still queued with no runner activity past the timeout gets its
  build probed — still provisioning/working is left alone; failed / vanished /
  finished-silent becomes `RunStartFailed` → terminal check failure.
- **Cutover** (the infra side, later): widen the repo webhook events
  (pull_request + push + check_run), strip the `github {}` blocks from the
  Cloud Build triggers (manual triggers post no native checks), add the
  executor block to the serve config, and have the build yamls consume
  `_EXECUTION_ID`. Until then nothing changes in production.

### Inbound Cloud Build awareness (recover builds serve didn't launch)

A build outside serve's own `StartRun` call — a native-check Re-run or a
console rebuild — used to leave the stuck `terraform/<env>` check pointing at
a run serve had already given up on, with no path back to green. `POST
/pubsub/cloud-builds` closes that gap reactively: it ingests the project's
`cloud-builds` Pub/Sub topic (OIDC-verified, reusing the same push verifier
and `PushServiceAccount` identity check as `/pubsub/push`) and always ACKs
(a wedged subscription from endless redelivery is worse than a dropped
build event).

- **Correlation**: `reconcileInboundBuild` recovers the owning execution in
  precedence order — `_EXECUTION_ID` (exact) → `_PR_NUMBER` (latest execution
  for that PR/env) → `(environment, context, sha)` via
  `store.FindExecutionBySHA`. A build that matches none of these, or whose
  trigger name isn't in `BuildTriggerNames`, is silently ignored.
- **Supersede + adopt**: `decideInboundBuild` fires only when the matched run
  is one serve has already given up on (start_failed / completed /
  superseded) and the build is a genuinely new `BuildRef` for the same SHA —
  a live run is left untouched, and a build serve already tracks is a no-op.
  It emits `RunSuperseded` + `RunAdopted`, and the shell's `AdoptRun` action
  materializes a fresh execution + check row (a new attempt) pointed at the
  existing build — **without** calling the executor, since the build already
  exists. The stale FAILED check is shadowed by the new in-progress one, and
  the existing start watchdog re-points to the adopted `BuildRef` and remains
  the fail-safe backstop for it.
- **Conclusion still comes from the runner.** Adoption only re-arms the
  check; the rich green/red conclusion is unchanged — it comes from the
  rebuild's own runner finalize. A rerun triggered outside `triggers.run` can
  report `pr=0` (its `_PR_NUMBER` substitution is lost); `handleInit` now
  recovers the owning PR by `(environment, context, sha)` — the same key the
  inbound-build path uses — before writing the row, so the rerun still
  supersedes and reattaches to the PR's check instead of orphaning.

**Known limitations / gotchas (inbound Cloud Build awareness):**

- **The conclusion still depends on the runner reporting.** Adoption only
  gets the check back to `in_progress` against the right build; if that
  rebuild's runner never reports (crashes before its first `Init`, wrong
  image, network partition), the check has no other path to a terminal
  state — the existing start watchdog is what eventually fails it safe after
  its timeout.
- **SHA-based correlation assumes plan head SHAs are PR-unique.** The
  `(environment, context, sha)` fallback (`FindExecutionBySHA`) picks the
  most recent non-superseded execution for that key; two open PRs sharing an
  identical head SHA (e.g. a branch pushed to both) would correlate to
  whichever execution was recorded, not necessarily the right one. The
  `_EXECUTION_ID`/`_PR_NUMBER` precedence exists specifically to avoid
  relying on this in the common case.
- **Inert until the infra companion lands.** `handleCloudBuildPush` 404s
  unless run triggering is armed, a push verifier is configured, and
  `BuildTriggerNames` is non-empty — but even fully configured, nothing
  reaches it until each tier's infra wires a `cloud-builds` push subscription
  to `/pubsub/cloud-builds` with the same push-SA OIDC identity/audience as
  the existing `/pubsub/push` subscription. No new serve GCP role is needed.
- **A console rebuild of an already-green SHA flips the check back to
  in-progress.** The inbound path fires for any run serve has given up on,
  which includes `completed` — so re-running an already-succeeded build from
  the Cloud Build console adopts it and moves `terraform/<env>` from success
  back to in-progress until the rebuild's runner reports. This is
  user-visible but intentional; serve's own builds never trigger it, since
  the `BuildRef`-match and live-run guards only let a genuinely new,
  serve-abandoned build through.

### Delivery: binary + Cloud Run container

The `serve` face is intended to run as a Cloud Run-class service, so a release
ships **two artifacts from one codebase**: the standalone binary (today's CLI
delivery — Homebrew/Docker) *and* an OCI **container image** whose entrypoint is
the same binary's `serve`. Because the binary is fully static (pure-Go SQLite,
no cgo) and embeds its assets (migrations, and later the UI CSS) via `go:embed`,
the image can use a minimal static/distroless base and needs no runtime files —
`go build` alone always works and consumers never need a CSS/SQLite toolchain.
The release GitHub Action builds and pushes a versioned, multi-arch
(`linux/amd64`+`linux/arm64`) image to GHCR alongside the per-platform binaries,
so a consumer points Cloud Run straight at
`ghcr.io/<org>/terraform-stack-plan:<tag>` with no per-consumer build. The image
job first ran for the **v0.6.0** release; Litestream replication and secret
mounting are deployment concerns documented there and in `SECURITY.md`.

The repo `README.md` is the user-facing guide for all four faces
(`render`/`run`/`serve`/`state`), and `examples/serve.tfstackplan.hcl` is the
control-plane config reference (kept valid by a `config` parse test,
`TestExampleServeConfigParses`).

## Future / deferred

- `tfplan2md` shell-out renderer behind a `--render` flag.
- Additional presets (`data`, `cluster`, `schema-migration`).
- Multi-part output (split into several comments) as an alternative to the
  terminal cascade's truncation.
- Run-to-run diffing (highlight what changed since the last report).
- Multi-tier in one comment.
- SARIF / static-analysis rollup.
