# Per-resource nesting in the rendered output

**Date:** 2026-05-29
**Status:** Approved (design)

## Problem

Inside a stack's `<details>`, nesting is inconsistent and visually weak:

- Creates and deletes render as nested `<details>` rows, but updates render as
  bare ```diff fences flush against the stack's left margin. The mix means there
  is no uniform "this is one resource" level.
- GitHub gives `<details>` only a disclosure triangle with almost no
  indentation, so even the nested create/delete rows don't *look* nested. A
  reviewer can't tell at a glance which stack/resource they're reading inside.

## Goal

Make the hierarchy **stack → resource** visually distinct and uniform, and make
the collapse decision consistent across all actions: small things show in full,
big things collapse to a row you expand.

## Design

### Structure (per stack)

Inside each changed stack's `<details>`, wrap the stack's body in a **blockquote**
so a left bar marks the stack scope. Every resource — create, delete, update,
replace — becomes one uniform collapsible row inside that bar:

```
▾ service-projects/app-dev · ✅ safe · 1 add, 1 change, 1 destroy
│ ▾ ~ google_storage_bucket.uploads · 2 changed
│     + labels.team    = "platform"
│     ~ retention_days = 9 → 32
│ ▸ + google_service_account.api · 12 attrs
│ ▸ - google_bigquery_dataset.legacy_events · 2 attrs
```

(The `│` is the GitHub blockquote left border.)

### Folding rule (unified, size-based)

One rule for every action. Render the resource's full body — aligned leaves for
leaf attributes plus the selected variant content for any block/line-diff
attribute — inside its `<details>`. The row is:

- **open** when the body is small (≤ `openThreshold` rendered lines), so small
  creates/deletes/updates show in full without a click;
- **closed** when the body is big, collapsing to a one-line row you expand.

`openThreshold` is a render constant; **10 lines** (matches the differ's existing
`foldThreshold` mental model). This replaces both the old action-based rule
(creates/deletes always collapsed) and the separate per-attribute
sub-`<details>` for large update attributes — folding now happens once, at the
resource level, keeping a clean two-level hierarchy.

### Resource row summary

The summary stays informative when collapsed:

- create → `+ <address> · N attrs`
- delete → `- <address> · N attrs`
- update → `~ <address> · N changed`
- replace → `± <address> · replace`

`N` counts the resource's attributes (`len(Change.Fields)`) for create/delete and
changed attributes for update.

### `--details` flag

Unchanged at the stack level (`open` | `closed` | `auto`). When `open`, resource
rows are forced open too. Otherwise per-resource open/closed is size-based as
above.

### Body rendering

A resource body is one ```diff fence containing the aligned leaves followed by
any block-field content (its selected, post-fit variant). No nested
per-attribute `<details>` — the resource row is the only fold level.

## GitHub-rendering risk and fallback

GitHub's rendering of a `<details>` **inside a blockquote**, with a fenced code
block inside, is finicky (HTML-in-blockquote + code fences can break). Therefore:

- **First implementation step verifies** the blockquote bar renders correctly on
  a real GitHub comment (via a throwaway gist or test comment), using the
  `verify` workflow.
- If it does not render cleanly, **drop only the blockquote wrapper** and keep
  everything else (uniform per-resource rows + size-based folding) — i.e. the
  flush "Option B" structure. The rest of the design is independent of this
  outcome.

This decision is recorded in the implementation as a single, isolated choice
(emit-blockquote yes/no) so it can be flipped without touching the folding logic.

## Scope / affected components

| Package | Change |
|---------|--------|
| `internal/render` | Wrap stack body in a blockquote (behind the verify gate); render every resource as a uniform `<details>` row; open/closed by body line count; remove the per-attribute sub-`<details>` (block content now inline in the resource body); `openThreshold` constant; resource-summary magnitude. |
| `internal/render` tests | Update for the new structure; assert open vs closed by size; assert summary magnitude; assert block content rendered in the resource body. |
| `cmd/tfstackplan` examples | Regenerate goldens; budgets likely need light retuning (structure changes byte sizes). Keep the four cascade invariants. |
| `README.md` | Update "What it looks like" to the nested structure. |

No changes to `model`, `plan`, `differ`, or `fit` — `differ` still produces leaf
and block fields; `fit` still degrades block variants under the byte budget; the
open/closed decision uses the post-fit selected content.

## Testing

- render unit tests: a small update/create/delete renders **open** with full
  body; a big one renders **closed** (just the summary row) — the size boundary.
- resource summary magnitude per action.
- block-field content appears in the resource body (not dropped, not a separate
  details).
- blockquote wrapper present (or absent, per the verified decision) — one test
  pinned to the chosen behavior.
- golden examples regenerated; the four cascade invariants
  (full / degraded / summary-only / minimal) still hold after retuning.

## Risks

- **Budget retuning.** The new wrapper/structure shifts byte sizes; the four
  example budgets may need adjustment with margin so each still demonstrates its
  mode (the per-scenario invariant assertions catch a boundary slip).
- **Blockquote rendering** (above) — mitigated by verify-first + fallback.
- **Deeply nested blockquote heaviness** is avoided: only one blockquote level
  (the stack), not a second per-resource bar.
