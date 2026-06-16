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

The config also carries optional **server/serve** blocks for the control plane
(ignored by `render`): `server { url, environment }`; `class "<name>" { backend,
entitlement, entitlement_scope, required }` binding a classification class to an
approval gate (`entitlement_scope` is `projects` by default, or `folders` /
`organizations` for a class that grants at a higher resource scope); and
`serve { db_path, public_base_url, use_checks, webhook_secret_env, github_app {
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

- **Privilege-backed apply** — `POST /api/gate/check` now returns
  `{"requester": "<sa-email>"}` (the leased PAM requester for the PR) on its 200
  success path. `tfstackplan run apply --impersonate-requester` reads that email,
  mints a short-lived `GOOGLE_OAUTH_ACCESS_TOKEN` via the IAM Credentials API
  (using the CI runner's ADC, which must hold `serviceAccountTokenCreator` on the
  pool SAs), and sets the env var so terraform runs **as the elevated requester
  identity**. An unapproved IAM change therefore fails at GCP (403), not only at
  the fail-closed gate pre-check. The flag is a no-op when the gate check returns
  an empty requester (gateless plan, or no server configured) — a gateless apply
  needs no elevation and proceeds as normal. See `docs/ci-integration.md` for the
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
- **Per-stack log streaming requires the terramate script to tee output to the
  configured `--log-file`.** The `LogPump` tails `<stack>/tfstackplan.log` (the
  default) in each stack dir; nothing is streamed unless the stack's terramate
  script writes that file (e.g. `terraform plan ... 2>&1 | tee tfstackplan.log`).
  This indirection exists because terramate's parallel `script run` interleaves
  every stack's output on one stream, which cannot be demuxed per stack.
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
  from a clean plan no longer applies when `reconciler_core` is on.
- **An `EXPIRED` grant on a never-active gate target stays `Pending` (not re-armed
  until the next plan).** When an `AWAITING`/`ACTIVATING` grant expires in place on
  a target that was never `ACTIVE`, the observe path leaves the gate `Pending`
  (`firstTerminalBlock` matches only `DENIED`/`REVOKED`; the `prevWasActive`
  downgrade does not fire) and does not re-request it (it still has a `GrantName`).
  It is **fail-closed** (apply stays denied while `Pending`) and a re-plan re-arms
  it (the Phase-1 carry-forward `Open()` guard), but a `GateTick` alone will not.
  Reload mirrors this: a persisted `EXPIRED` target reconstructs as `Pending`, not
  `Blocked` (the flat row can't tell a never-active expiry from a was-active
  downgrade). Auto re-request of a lapsed grant on the observe path is queued for a
  later phase (the orphan/observe-path re-arm theme).
- **The `gcp-pam` grant justification correlates by `environment`, which must be
  whitespace-free.** The backend encodes the change as `PR #<n> env=<env>` and
  parses it back to map a grant to its `(PR, environment)`; the `env` token is
  read up to the first whitespace. Environments are slugs (`staging`/`prod`), so
  this holds, but an environment containing a space would not round-trip
  (correlation, reuse, and revoke would miss the grant).
- **The `gcp-pam` requester pool leases one SA per (PR, environment), reused
  across all of that PR's gates.** `RequestGrant` picks a pool identity with no
  open grant on the entitlement (falling back to `PR mod pool-size` when
  exhausted), so collision is bounded by *concurrent open PRs exceeding pool
  size*, not by PR-number arithmetic. Once the first grant fixes the identity the
  rest of the PR's grants share it. With `reconciler_core` on, the leased
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
land later) — see [PR #19](https://github.com/Fluent-Health/terraform-stack-plan/pull/19):

- A stdlib `net/http` `ServeMux` (Go 1.22 method routing — no router dependency)
  with a public `GET /healthz` and bearer-authed `POST /api/{init,phase,update,
  finalize}`. An empty configured secret disables auth (local/dev).
- A `GitHub` interface (create/update one check run per environment, post a
  commit status, read PR head SHA) with a `MockGitHub` test double; the real
  client is now implemented (see *Real GitHub client* below).
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

**Live UI (Phase-1 parity)** (see [PR #22](https://github.com/Fluent-Health/terraform-stack-plan/pull/22)).
The server serves a public, dependency-free live view: `GET /img/<id>.svg`
renders the execution as a **self-contained, inert** SVG dependency graph
(nodes in longest-path dependency layers, coloured with GitHub's light-theme
status hues — no `<script>`/`<foreignObject>`, so it survives GitHub's image
proxy), and `GET /live/<id>` is an auto-refreshing HTML page embedding that
diagram,

The live DAG (both `/live` and the `/img` GitHub image) now renders at the
**group** level rather than per-stack: stacks fold into groups by their first
`GroupDepth` path segments (`Config.GroupDepth`, default 2 → env/kind), each
group a single node showing its stack-count and **worst status** (with a
`N failed`/`N gated` tally), and edges are the terramate before/after
dependencies aggregated to the group level (intra-group and self edges dropped,
duplicates collapsed). The folding lives in `buildGroupGraph`
(`internal/server/grouping.go`); `renderGroupSVG` lays the group DAG out in
**horizontal lanes per environment** (the first segment of the group key,
e.g. `nonprod`/`prod`), sharing one dependency-depth column grid — so
duplicated-across-env work reads side by side. Each lane has a bold label at
its top. The shared `layersOf` helper assigns each node its column; the new
`laneOf` helper extracts the environment segment. Lanes are sorted
alphabetically; within a lane, nodes in the same column are sorted
alphabetically. The existing per-stack `renderSVG` is unchanged (it has no
lane concept). Each group node also renders a **category-badge line** (e.g.
`🔐 12  💣 5`, icon when present else name, name-sorted) aggregating its
stacks' matched classification categories —
plumbed per-stack from the `run plan` sidecar via `Finalize.Categories` → the
`stacks.categories` column → `LoadGraph` → `buildGroupGraph`'s per-group
`catCount` tally → `groupBadges`. Grouping is configurable via a
`serve { group { depth = N | pattern = "regex" } }` block: `depth` overrides the
default of 2; `pattern` overrides depth entirely — the group key is the regexp's
first capture group (or whole match if it has none), and non-matching paths are
their own group. The pattern is compiled once in `New` into `App.groupRE`; an
invalid pattern is logged and falls back to depth grouping. The folding stack list
on `/live` is grouped by the same key, serving as the DAG's per-stack drill-down
(expand a group → its stacks, each linking to the per-stack detail page). A
generic **approval panel** (one row per stored `(class, target)` with
its state — provider-neutral, no console URL; the deep link is added by the
approval backend), and the plan report (shown as escaped preformatted text — no
markdown engine). Both routes are public (read-access model: same sensitivity as
plan output already on the PR, behind unguessable execution ids); `/api/*` stays
bearer-authed.

The page is rendered with `html/template` from `internal/server/templates/`
(parsed once into `App.tmpl` in `New`) as a DaisyUI shell that links a committed
**Tailwind v4 + DaisyUI** stylesheet, served at `GET /assets/app.css`. The SVG
and approval panel are trusted server-generated HTML, injected un-escaped
(`template.HTML`); the repo, title and report body are auto-escaped. The live
page now subscribes to `GET /live/<id>/events` (an SSE endpoint that emits a
`changed` event on every execution state mutation) via an inline `EventSource`;
on receipt, a debounced `location.reload()` fires after 800 ms. The 10s
`<meta http-equiv="refresh">` is retained only as a `<noscript>` fallback for
environments without JavaScript. Client-side partial re-render (no full reload)
is a follow-on. Regen contract: the CSS is built in
`web/` with yarn and the **committed** `internal/server/assets/app.css` (`go:embed`-ed)
is the source of truth — `web/build.sh` regenerates it on demand; nothing in the
Go build or CI runs node. (PR #TBD.)

The live page builds on this shell with three reviewer-oriented sections, fed by
pure view-helpers in `internal/server/livedata.go` and a `liveView` input struct
in `livepage.go`. A **phase timeline** (DaisyUI `steps`) shows lifecycle progress
(`phaseTimeline` marks phases done/active/todo relative to the execution's current
phase). A **folding stack list** (`groupStacksByKey` — groups alphabetical) renders
each stack as a status badge (`statusBadge`, registered as a template func), its
path, and optional failure detail. The list is grouped by the **same path key as
the group DAG** (`groupKey` at `GroupDepth` / `groupRE`), so expanding a group in
the list drills into the stacks that make up the corresponding DAG node. The
dependency-graph SVG sits in a **collapsible** strip. Stack paths/statuses/details
and phase names are auto-escaped.

The renderer/page are deliberately minimal — the richer UI v2
(grouped/folding list, SSE log streaming, per-stack Log/Plan/Verify tabs,
hand-rolled diff renderer, cluster containers, pan/zoom, dark toggle) is a
separate later phase that replaces them behind the same routes.

**Approval gate** (see [PR #23](https://github.com/Fluent-Health/terraform-stack-plan/pull/23)).
`internal/approval` defines the provider-neutral gate abstraction: a `Backend`
(`RequestGrant`/`ListGrants`/`Revoke`) over a `Request{Class, Target, PR,
Environment}` and a normalised `GrantState` (AWAITING → ACTIVATING → ACTIVE, plus
DENIED/REVOKED/EXPIRED). The server only ever *requests*; humans approve in the
backing provider. An in-memory `Fake` makes the whole gate flow e2e-testable.
The server wires it via an optional `App.Approval` field (nil disables gating —
gates park at `action_required`): at finalize it requests a grant per `(class,
target)` gate and records the grant name + state; `reconcileGate` refreshes each
target's state from the backend and, once all are `ACTIVE`, flips the gated
stacks safe and re-drives the check run to `success`; a periodic `ReconcileLoop`
runs that over `PendingGates`, self-healing the activating→active transition with
no provider event required. The apply path uses `POST /api/gate/check`
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
are in `docs/deploy-cloud-run.md`; the hardening notes are in `SECURITY.md`.
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
registers the apply execution, applies the changed stacks in dependency order
(the terramate `apply` script, no `--parallel`), and revokes the PR's grants
afterward (best-effort, whether or not the apply succeeded). Tested end to end
against real terramate + a stub `terraform`: gate satisfied → apply runs in DAG
order + revoke; gate blocks → abort before any apply. The `--impersonate-requester`
flag (see *Shipped since v1 — Privilege-backed apply*) is additive on top of this
increment; the gate pre-check behaviour is unchanged with the flag on.

The sixth increment landed the **CI integration guide** (`docs/ci-integration.md`):
the consumer-facing wiring — the `TFSTACKPLAN_*` environment, the terramate
`plan`/`apply` scripts that run terraform + `run tick`, and example plan-on-PR /
apply-on-merge CI jobs that each shrink to checkout + one `run` invocation. With
this, **Phase 2 is complete**: `tfstackplan run` drives plan/apply end to end,
reporting to the `serve` control plane, while terraform keeps executing in the
consumer's own CI under their own identities.

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
With `App.Objects` unset, behavior is unchanged (buffer-only). _Remaining log
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

The sixth increment wired up **per-stack detail pages**: each stack row on the
`/live/<id>` page now links to `GET /live/<id>/stack/<stack...>`, which serves
three tabs — **Log**, **Plan** (the stored per-stack plan section from step five),
and **Verify** (a Phase 4 placeholder). `liveView` carries the execution id
(`Exec`) so the template can build the hrefs; `handleLive` sets it from `e.ID`.

The Log tab live-tails via `EventSource` on `/logs/<exec>/<stack>?follow=1` (the
hardened SSE stream from Phase 3's second increment — id-tagged, Last-Event-ID
resumable, heartbeat-kept, with buffer replay on reconnect). The `<pre
id="stacklog">` element carries a `data-follow-url` attribute (set in HTML
context to avoid JS-context escaping), which the inline script reads to open the
`EventSource` and append each `onmessage` line. A `<noscript>` block falls back
to the stored tail excerpt (or "No log captured" when absent).

The final UI-v2 increment adds **execution navigation**: a landing index at
`GET /{$}` (the most recent executions, newest first) and a per-PR timeline at
`GET /pr/{n}`. Both render a shared `executions.gohtml` DaisyUI table whose rows
link to `/live/{id}` (and back to `/pr/{n}`), backed by `store.ListExecutions`/
`store.ListExecutionsForPR` over the indexed `created_at`; `handlePRTimeline`
returns 400 on a non-numeric PR. This completes Phase 3's UI v2.

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
- **`apply/<env>` check run.** `handleInit`/`handlePhase` now call `ensureCheckRun`
  for apply contexts (in addition to the existing gate-context path), creating a
  check run named after the status context (e.g. `apply/nonprod`). `driveApply`
  updates that check run with progress and a terminal conclusion. The existing
  commit status is preserved alongside it.

**Phase 4: `run verify` (complete).** `tfstackplan run verify` runs the
terramate `verify` script across changed stacks — no gate, read-only
post-apply validation. It streams per-stack logs via the tail-pump (same
`LogPump` as plan/apply) and registers a `verify/<env>` execution on the
server, reported under a `PhaseVerifying` phase that joins the execution
timeline. The per-stack Verify tab on `/live/{id}/stack/{stack...}` shows the
latest verify run's per-stack output **inline**, live-tailed via a
`<pre id="verifylog" data-follow-url="/logs/{verifyExec}/{stack}?follow=1">` element
(the same SSE stream used by the Log tab), with a noscript tail-excerpt fallback
and a run link to `/live/{verifyExec}`. The follow script now iterates all
`[data-follow-url]` elements so both `#stacklog` and `#verifylog` are tailed in
parallel. The latest verify run is resolved via
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
execution-lifecycle state. Its single entry point —
`Step(World, Signal) → (ChangeSet, []Action)` — is a total function with no I/O:
given the current observed world and an incoming signal (plan finalized, grant
observed, revoke requested, etc.) it returns a set of state changes and a list of
idempotent actions to execute. The imperative shell (`internal/server/shell.go`)
wraps it: gather a scoped `World`, call `Step`, execute the returned `Action`s
(persisting each result back as a `GrantsObserved` signal), persist the `ChangeSet`,
and repeat to a fixpoint — serialized per `(pr, environment)`.

`GateState` is a proper sum type: `NotClassified` (PR never finalized — apply fails
closed), `Clean` (classified, zero gate targets — apply passes), `Pending` (grants
in flight), `Satisfied` (all grants `ACTIVE`), `Blocked` (denied/expired/revoked or
slot-collision). `NotClassified` ≠ `Clean`, removing the old ambiguity. The leased
requester SA lives inside the gate variant, not in a separately clobberable column,
so the clobber bug that existed when a re-plan called `SetTargetRequester` is
structurally impossible. The per-stack `gated`/`safe` overlay written to
`stacks.status` by `save()` is a **derived** projection of `GateState` — it is not
separately tracked truth.

The engine ships behind the off-by-default `reconciler_core` serve-config flag. It
engages only at quiescence (`tfstackplan serve --check-quiescent`, with a startup
guard that falls back to the legacy engine if the store has in-flight rows). Legacy
gate handlers remain as the flag-OFF path pending a post-cutover cleanup.

The permutation test harness (`internal/reconcile/step_table_test.go`) is the
correctness oracle, covering all state permutations as a table-driven suite. It
fixed six latent invariant gaps — ACTIVE-grant downgrade, re-plan pruning, Blocked
surfacing for DENIED/REVOKED, revoke persistence, per-ChangeSet serialization, and
same-PR cross-env slot self-deadlock — see PR for the full reasoning.

Phase 1 boundary hardening (PR for this branch) closed three further gaps. A
re-plan now re-arms a terminally-blocked gate target: `stepFinalize` carries a
prior grant forward only while it is still `Open()`, so a DENIED/REVOKED/EXPIRED
target re-enters the request cycle on the next plan instead of wedging (re-arming
on a fresh plan, not on every tick, so a standing denial does not auto-retry).
The grant-observation fold is deterministic on equal rank (rank → lease-requester
match → greater grant name), and the lease is never pinned from a terminal grant.
These three are coherent on a single `Open()` (live vs terminal) distinction.

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
      (`source_stack` + `from`/`to` address pairs, one manifest per dest+source),
      executed later by `state apply` (see below) rather than by `run apply`.

  Blocks are written to a PR-keyed shim `_tfsp_move.<key>.tf` in each affected
  stack dir; the normal `run apply` (backend-locked, no state surgery) applies
  them. Ops accumulate into the shim across invocations (existing blocks are
  merged, not clobbered). The key is `PR-<n>` from `--pr` / `$TFSTACKPLAN_PR`,
  else `branch-<name>` from the git branch, else `local`.
- `state list [--dir DIR] [--pr N]` lists discovered shims (`key`, stack, and a
  kind-aware op line: `moved from → to`, `import to (id=…)`, or `removed from`).
- `state cleanup --dir DIR (--pr N | --all)` removes the keyed shims (one PR's,
  or all `_tfsp_move.*.tf` in the tree).
- `state apply --dir DIR [--execute] [--lock]` discovers every
  `_tfsp_xmove.*.hcl` manifest and runs it via terraform-exec: pull both states
  → back up each (`<dir>/.tfsp-state-backups`) → per-pair fail-closed
  decision table (source-only → **move**, dest-only → **skip** (idempotent),
  both/neither → **error**) → `terraform state mv -state/-state-out` against the
  pulled local files → push both, **never** `--force`. **Dry-run by default**
  (prints "would move" / "skip"); `--execute` performs the moves. Requires
  `terraform` on `PATH`. The discover→execute→print core is the package-level
  `applyPendingMoves`, shared with the `run apply` pre-phase (below).
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
`_tfsp_xmove.*` namespace that cannot be parsed, or whose `# tfstackplan:key=`
header disagrees with its filename, errors the read path (the classify pass,
`state apply`, `state list`, `moves-manifest`) rather than being silently
skipped — a silently dropped manifest would let a relocation classify as (and
apply as) a real destroy+create. The filename is the authoritative key; `state
cleanup` matches by filename and does not parse, so a corrupt or key-mismatched
file is always removable.

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
