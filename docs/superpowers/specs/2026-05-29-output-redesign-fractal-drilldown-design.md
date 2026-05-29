# Output redesign — fractal per-resource drill-down

**Date:** 2026-05-29
**Status:** Approved (design)

## Problem

The per-stack output has three shortcomings:

1. **Creates and deletes show only the address.** `+ google_service_account.api`
   tells you nothing about *what* is being created, and `- google_bigquery_dataset.x`
   nothing about what is being destroyed.
2. **Updates are misaligned.** Each changed attribute renders independently, so a
   nested-map attribute (e.g. `labels`) becomes an indented, orphaned
   `  + team: platform` with its attribute name dropped, while a scalar renders
   flush-left — a ragged left margin with no grouping.
3. **No drill-down.** Everything in a stack is dumped into one flat ` ```diff `
   block. There is no way to scan a stack and expand only the resource you care
   about ("fractal" reading).

## Goal

In normal operation, looking at the rendered comment should be all a reviewer
needs — but **fractal**: a scannable surface that drills down where wanted, not
everything at once. Concretely:

- Creates show their full attribute set; deletes show what is being removed.
- Updates show their changed attributes, aligned and grouped.
- Large diffs (and create/delete attribute dumps) fold behind a click.
- The update view is aligned and grouped.

## Design

### Per-resource rendering

Inside each stack's `<details>` (still closed by default; `--details
auto|open|closed` unchanged), resources render by action:

**Create / Delete** — the whole resource folds into a nested `<details>`:

```
▸ + google_service_account.api · 3 attrs
▸ - google_bigquery_dataset.legacy_events · 3 attrs
```

Expanded body (one ` ```diff ` fence) lists every attribute, nested maps
flattened to dotted paths, aligned on `=`:

```diff
+ account_id   = "app-api"
+ display_name = "API service account"
+ disabled     = false
```

Attributes come from the plan's `after` (create) or `before` (delete) — new data
the parser must now extract.

**Update / Replace** — a header line plus the changed attributes:

```diff
~ google_storage_bucket.prod_state
    + labels.team    = "platform"
    ~ retention_days = 7 → 30
```

- Each changed leaf carries its own `+` / `~` / `-` and a dotted path; `=` is
  aligned within the resource block. This fixes the misalignment.
- Replace renders like an update with a replace marker on the header
  (`± <address> · replace`).
- A **small** attribute renders inline (as above). A **large** attribute (its
  rendered diff exceeds the fold threshold) folds into its own nested
  `<details>`:

```
~ kubernetes_config_map.app_config
   ▸ ~ data · 90 lines changed
```

### Structured (JSON / YAML) string attributes

The differ already detects JSON/YAML string values and renders a **structural**
(changed-paths-only) diff. That behaviour is preserved and benefits directly
from the new per-leaf format:

- A big YAML/JSON field where only a couple of keys change renders as just those
  dotted-path lines — the readability win, shown **inline** because it is small:

  ```diff
  ~ kubernetes_manifest.ingress
      ~ spec.rules[0].host          = "old.example.com" → "new.example.com"
      ~ spec.tls[0].secretName      = "old-tls" → "new-tls"
  ```

- The same field with many changed keys exceeds the threshold and **folds**:

  ```
  ~ kubernetes_manifest.ingress
     ▸ ~ manifest · 47 paths changed
  ```

This must be visible in the examples (see below).

### Sensitive / known-after-apply

Inline, in the new `=` style: `~ path = (sensitive value)` /
`~ path = (known after apply)`. Semantics unchanged.

### Fold threshold

A constant, ~10 rendered lines. Below it, inline; at/above it, fold. Could later
be surfaced in the `diff {}` HCL block; out of scope for this change.

### Size budget interaction (unchanged)

Folding into `<details>` does **not** save bytes — GitHub counts hidden content
toward the 65,536-byte cap. So the `fit` cascade is unchanged: it still degrades
the largest attribute variants to summary lines, then drops to summary-only,
then minimal. Folding (a reading affordance) and fit (a byte mechanism) compose:
a large attribute is folded into a `<details>` and, under budget pressure, its
content is degraded to a summary line *inside* that collapsed block.

Adding create/delete attributes makes reports larger, so example goldens grow
and budgets are retuned.

## Affected components

| Package | Change |
|---------|--------|
| `internal/plan` | Extract changed attributes for **create** (`after`) and **delete** (`before`), not just update/replace; carry sensitive/unknown markers. |
| `internal/differ` | New aligned `key = value` / `→` format; per-leaf `+`/`~`/`-` for structural diffs; per-resource alignment; expose the selected variant's line count so render can make the fold decision. Preserve JSON/YAML/base64 detection. |
| `internal/model` | `Change.Attrs` populated for all actions; render carries enough to decide folding (per-attr line count is already implied by variant `Bytes`/content). |
| `internal/render` | Per-resource grouping; nested `<details>` for create/delete and for large update attributes; alignment assembly; multiple diff fences. |
| `internal/fit` | Verify largest-first degradation still holds over the new variant set; adjust only if grouping changes byte accounting. |
| `cmd/tfstackplan` (examples) | Regenerate goldens; **add a fixture** exercising a structured (YAML/JSON) string attribute, in both the few-keys-changed (inline, compact) and many-keys-changed (folded) forms. |
| `README.md` | Update "What it looks like" to the new structure. |

## Testing

- Existing `internal/render`, `internal/differ`, `internal/plan`, `internal/fit`
  unit tests updated for the new format.
- `differ`: assert create (after-only) and delete (before-only) attribute
  extraction; aligned output; per-leaf symbols on structural diffs; the
  JSON/YAML structural path renders dotted leaf changes.
- `render`: nested `<details>` for create/delete; inline vs folded update
  attributes at the threshold boundary; alignment.
- Golden examples (`go test ./cmd/tfstackplan`) regenerated, now including the
  structured-string scenario. Cascade examples (degraded / summary-only /
  minimal) retuned and re-asserted.

## Risks

- **Byte-budget retuning.** New attributes shift sizes; the four cascade
  example budgets need re-tuning with margin so each still demonstrates its
  mode (the per-scenario invariant assertions catch a boundary slip).
- **Alignment vs `fit` interplay.** Per-block alignment computes padding from the
  full attribute set; when `fit` degrades some attributes, alignment is over the
  rendered (post-fit) lines so padding stays correct. Render must align after
  variant selection, not before.
