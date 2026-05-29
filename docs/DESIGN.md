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

The tool is a **pure renderer**: it reads `plan.json` files and writes markdown
(and an optional sidecar JSON). It does not run `terraform plan` and does not
post to GitHub — the CI pipeline does that.

## Goals (v1)

- Merge many `plan.json` files into one marker-keyed markdown document.
- Top-level summary table with per-stack action counts.
- Collapsed per-stack `<details>` with a built-in rendered diff.
- **Optional** classification: tag each stack as `iam` / `safe` / custom classes
  via a declarative ruleset, primarily to gate IAM changes in CI.
- Built-in, configurable presets for common rulesets (`iam` now).
- Comment-size budget handling (GitHub's 65,536-byte cap).
- Sidecar JSON of computed classes for CI gating logic.

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
| 3 | **Manifest format: YAML** (JSON is a subset) | Human-writable, common in CI. The manifest is per-run/ephemeral. |
| 4 | **Classification lives in a separate HCL config file** | Classification is *repo policy* (stable, git-tracked), with a different lifecycle from the per-run manifest. HCL is idiomatic for the Terraform ecosystem (cf. `.tflint.hcl`, terramate's `.tm.hcl`). |
| 5 | **Presets** as built-in named rule bundles | Repos opt into `iam` (and future `data`, `cluster`) without rewriting regexes. |
| 6 | **Declaration-order rule evaluation, first-hit-wins** | `preset` and `rule` blocks evaluate top-to-bottom; explicit and one mental model. |
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

- **load** — read the manifest, every `plan.json`, and the HCL config.
- **gather** — build a complete, **budget-agnostic** `Model`: per-stack action
  counts, the classification (class + icon), and for every changed attribute an
  ordered list of candidate render *variants* with their byte sizes.
- **fit** — pure `Fit(Model, budget) → Model'`: pick one variant per attribute
  so the estimated total fits, degrading the largest first. Touches diff depth
  only; never the summary table or classification.
- **render** — pure `Model' → markdown`.
- **write** — stdout/file, plus the optional sidecar JSON (taken from the model
  *before* fit, since classification is never reduced).

```
cmd/tfstackplan/   — CLI entry; orchestrate load → gather → fit → render → write
internal/manifest/   — load + validate per-run YAML/JSON manifest and --stack flags
internal/config/     — parse + validate the HCL config (classification {} + diff {})
internal/presets/    — built-in named rule bundles (iam, …) as []classify.Rule
internal/plan/       — parse plan.json (terraform-json) → action counts + raw attr changes
internal/classify/   — apply resolved ordered rules → Class{Name, Icon}  (gather)
internal/differ/     — type detect + emit ordered render variants per attribute  (gather)
internal/model/      — Model/Stack/Change/AttrDiff/Variant types (the shared spine)
internal/fit/        — pure budget reduction over the model (largest-first degradation)
internal/render/     — Model' → markdown
```

**Key boundaries:**

- `config` + `presets` resolve into a single ordered `[]classify.Rule`.
  `classify` consumes that list and a parsed plan and returns `Class{Name,Icon}` —
  it neither knows nor cares whether a rule came from a preset or a custom block.
- `differ` owns all type-specific knowledge (JSON/YAML/base64/plain) and emits,
  per attribute, an ordered `[]Variant{Level, Bytes, Content}` from preferred →
  minimal. `fit` is generic arithmetic over those variants and knows nothing
  about YAML; `render` just emits the chosen variant. A per-attribute config
  override only changes which variants `differ` emits — `fit` and `render` are
  untouched.

## Inputs

### Manifest (per-run, YAML or JSON)

```yaml
title: "Terraform plan — nonprod"
marker: "tfstackplan:nonprod"
stacks:
  - name: platform/nonprod
    plan: ./out/platform-nonprod/plan.json
  - name: service-projects/app-dev
    plan: ./out/app-dev/plan.json
```

### Or via flags

```bash
tfstackplan \
  --stack platform/nonprod:./out/platform-nonprod/plan.json \
  --stack service-projects/app-dev:./out/app-dev/plan.json \
  --title "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

`--stack` is `NAME:PATH` (no class suffix; classification comes from config).

### CLI surface

```
tfstackplan [--manifest FILE | --stack NAME:PATH ...]
              [--config FILE]                  # HCL policy (classification {} + diff {}); auto-discovers .tfstackplan.hcl in CWD
              [--title TEXT]
              [--marker TEXT]
              [--max-bytes N]                  # default ~60000 (under GitHub's 65536 cap); 0 disables
              [--details auto|open|closed]     # default: closed
              [--emit-classification-json FILE]
              [--output FILE | -]              # default: stdout
```

- `--manifest` and `--stack` are mutually exclusive ways to list stacks.
- `--config` overrides config discovery. If neither `--config` nor an
  auto-discovered `.tfstackplan.hcl` exists, **classification is off** (no
  class column / icons) and `diff {}` falls back to defaults (detection on, no
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
| `["no-op"]`                       | *ignored* |

`no-op` changes are not counted and not rendered.

### Summary table

```markdown
<!-- tfstackplan:nonprod -->
### Terraform plan — nonprod  (3 stacks changed)

| Stack                          | Add | Change | Destroy | Class   |
|--------------------------------|----:|-------:|--------:|---------|
| platform/nonprod                  |   0 |      1 |       0 | 🔐 iam  |
| service-projects/app-dev    |   2 |      0 |       0 | ✅ safe |
| service-projects/app-test   |   0 |      0 |       0 | ✅ safe |
```

- Columns: `Add`, `Change`, `Destroy`, `Replace`. **Any column that is zero
  across all stacks is omitted** (so the common no-replace case shows three
  count columns).
- The `Class` column appears only when classification is enabled.
- "(N stacks changed)" counts stacks with ≥1 non-no-op change.

## Classification (optional)

### Discovery

`--config FILE`, else auto-discover `.tfstackplan.hcl` in the current working
directory. Absent → classification disabled.

### HCL schema

```hcl
# .tfstackplan.hcl  (repo policy — checked into git)
classification {
  default {                 # the fallback class when no block matches
    name = "safe"
    icon = "✅"
  }
  # shorthand: `default = "safe"` is equivalent with no icon.

  preset "iam" {            # built-in bundle; expands to its rules at this position
    icon = "🔐"             # optional: override the preset's default glyph
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
| `name` (rule label / preset name) | The class name shown in the table                                   | required    |
| `icon`                  | Glyph prepended to the class name                                             | none        |
| `resource_type_pattern` | Regex matched against each change's `type` (e.g. `google_compute_instance`)   | `.*` (any)  |
| `actions`               | List of action strings; rule matches a change if **all** listed appear in its `actions[]` | any action |
| `min_count`             | Minimum number of matching changes for the rule to apply                      | 1           |

- A change with `update` matches a rule whose `actions` is unset — so an in-place
  IAM-policy update classifies as `iam`, not just creates. (The `iam` preset
  leaves `actions` unset by design.)
- Rules with no matcher fields are catch-alls.

### Evaluation order

`preset` and `rule` blocks evaluate **top-to-bottom in source order**; a
`preset` block expands to its bundled rules at its declared position. First rule
whose matcher is satisfied for a stack sets that stack's class (name + icon). No
match → the `default` class.

### Presets (built-in)

- **`iam`** (v1): one rule matching IAM resource types across providers, e.g.
  `_iam_(policy|binding|member|audit_config)$` for google/google-beta,
  `^aws_iam_`, `^azurerm_role_(assignment|definition)$`. `actions` unset (any).
  Default class name `iam`, default icon `🔐` (overridable via the block's `icon`).
- Future bundles (`data`, `cluster`) follow the same shape; out of v1 scope but
  the `presets` package is structured to add them as named `[]Rule`.

### Sidecar JSON

```bash
tfstackplan --manifest plan.yaml --output report.md \
              --emit-classification-json classes.json
```

```json
{
  "platform/nonprod":              { "class": "iam",  "icon": "🔐" },
  "service-projects/app-dev": { "class": "safe", "icon": "✅" }
}
```

Lets a CI pipeline drive gating off the same source of truth that renders the
comment (e.g. "if any class is `iam`, require a PAM grant before merge") — no
re-grepping the markdown.

## Rendering, the differ, and the size budget

### Why `<details>` is not a size lever

GitHub's 65,536-byte comment limit applies to the raw markdown *source* —
content hidden inside a collapsed `<details>` still counts in full. So
collapsing helps *reviewer ergonomics* (not scrolling past 40 diffs) but does
**not** save bytes. Fitting the budget therefore requires actually
*summarizing or omitting* content, which is what `fit` does.

### Document shape

```
<!-- tfstackplan:nonprod -->     ← marker, always line 1 (CI upsert key)
### Terraform plan — nonprod  (3 stacks changed)
| summary table |
<details>…per stack…</details>     ← closed by default; --details = auto|open|closed
```

Each stack's `<details>` heading mirrors the README: `platform/nonprod · 🔐 iam ·
1 change`. The body is a single fenced ` ```diff ` block (GitHub colorizes
`+`/`-`), grouped by action.

### The differ — ordered variants per attribute

For each changed attribute, `differ` detects the value type and emits an ordered
list of render variants, **preferred → minimal**, each with a precomputed byte
cost:

| Detected type            | Variant ladder (preferred → minimal)          |
|--------------------------|-----------------------------------------------|
| JSON / YAML (structured) | `Structural` (changed paths only) → `Summary` → `Hidden` |
| Plain multi-line text    | `LineDiff` (limited context) → `Summary` → `Hidden` |
| base64 / binary          | `Summary` (byte delta) → `Hidden`             |
| scalar / sensitive / known-after-apply | single inline variant (`a -> b`, `(sensitive value)`, `(known after apply)`) |

- **Structural is the *preferred* start for structured types**, not a fallback —
  a 400-line manifest with one changed field renders as one path line.
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
  asserting class + icon and first-hit-wins / default fallback / `min_count` /
  `actions`-all-present semantics.
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

## Future / deferred

- `tfplan2md` shell-out renderer behind a `--render` flag.
- Additional presets (`data`, `cluster`, `schema-migration`).
- Multi-part output (split into several comments) as an alternative to the
  terminal cascade's truncation.
- Run-to-run diffing (highlight what changed since the last report).
- Multi-tier in one comment.
- SARIF / static-analysis rollup.
- First-class handling of the Terraform `forget` action (`removed {}` blocks). v1 excludes `forget` from counts/rendering like `no-op`/`read` to avoid mislabeling it as an update; surfacing it as its own bucket is a future enhancement.
