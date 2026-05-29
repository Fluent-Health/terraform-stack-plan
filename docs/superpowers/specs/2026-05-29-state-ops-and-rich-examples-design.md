# Surface moved / imported / removed-from-state, and richer examples

**Date:** 2026-05-29
**Status:** Approved (design)
**Builds on:** PR #2 (per-resource nesting). Lands on `render/per-resource-nesting`.

## Problem

1. The parser drops `no-op`, `read`, and `forget` changes, so **moved**,
   **imported**, and **removed-from-state** (`forget`) resources are invisible —
   even though the plan JSON carries `PreviousAddress`, `Change.Importing`, and
   the `forget` action.
2. The structured-diff examples are thin: a single flat 12-key `spec:` map. We
   want **realistic nested JSON and YAML** examples — small changes (inline,
   with array indices) and big changes (folded) — plus a nested-block example.

## Design

### Parsing (`internal/plan`, `internal/model`)

Stop dropping the cases we want; keep dropping pure `no-op` and `read`.

- **forget** (`actions:["forget"]`) → new `model.ActionForget`. Extract `before`
  attributes (what's leaving state) via the existing `sideAttrs(_, false)`.
- **moved** (`rc.PreviousAddress != "" && != rc.Address`) → annotate the change:
  `Moved bool`, `PreviousAddress string`. Works with any underlying action
  (pure rename = no-op; move+update = update).
- **imported** (`rc.Change.Importing != nil`) → annotate: `Imported bool`,
  `ImportID string` (`Importing.ID`).
- A change that is **no-op but moved/imported** gets new `model.ActionNoop` so it
  renders without counting as add/change/destroy/replace. Pure no-op (no
  move/import) and `read` are still skipped.

`RawChange` gains `Moved`, `PreviousAddress`, `Imported`, `ImportID`;
`model.Change` carries the same. `bucketOf` is only called for real actions; the
`Parse` switch routes forget/no-op explicitly.

### Counts (`internal/model`)

`Counts` gains `Move`, `Import`, `Forget int`. `Parse` increments `Move`/`Import`
from the annotations and `Forget` for `ActionForget`. `AnyChange()` returns true
when `Total() > 0 || Move+Import+Forget > 0`, so a move-only stack still renders
and counts in "(N stacks changed)".

### Rendering (`internal/render`) — rows, table choice B

Resource row label, by precedence **forget → moved → imported → underlying**:

- `⊘ <addr> · forgotten · N attrs` — body lists the before-attributes, each
  prefixed `⊘` (render-level override so it reads as "leaving state", distinct
  from a destroy's `-`).
- `↪ <addr> · moved from <prev>` (append `, N changed` when also updated) — body
  shows the update diff if any, else a dim `(address change only)`.
- `⤓ <addr> · imported (id="<id>")` (append `, N changed` when also updated).
- otherwise the existing `+/~/-/±` create/update/delete/replace label.

`ActionNoop` rows carry only the move/import label + (for moves) any diff.

Open/closed still uses the size rule from PR #2 (small body open, big folded).

**Summary table (choice B): no new columns.** When a stack has any
move/import/forget, append a dim suffix to its Stack cell, e.g.
`platform/prod · 1 import, 1 move, 1 forget`. The per-stack `<details>` heading's
count phrase (`changeWord`) is extended the same way (`4 change, 1 import, 1
move, 1 forget`). Wording matches the existing singular style (`1 import`,
`2 import`).

### Classification

`classify` already iterates the stack's changes by `type`/`actions`. Including
the new rows means a custom rule can target them (e.g. `actions = ["forget"]`).
No change to the classifier; the default class still applies to no-op
moves/imports that match no rule.

### Examples (the richer part)

Add generator helpers producing **realistic nested** structures, each with a
small-change (inline structural leaves, incl. array indices) and a big-change
(folded) variant:

- **YAML manifest** — a Kubernetes Deployment: `spec.template.spec.containers[0]`
  (`image`, `resources.limits.cpu`), `env[N].value`, `replicas`. Small: bump
  `image` + `replicas` (2–3 paths → inline). Big: change many env/limits keys
  (≥ foldThreshold → folded). Paths render like
  `~ manifest.spec.template.spec.containers[0].image = "app:1.4" → "app:1.5"`.
- **JSON policy** — an IAM policy document: `Statement[N].Effect/Action/Resource`.
  Small: change one `Resource` (inline). Big: rewrite many statements (folded).
- **Nested block** (HCL, not a string) — a `google_compute_firewall` with an
  `allow` block list: `allow[0].ports[0]`, `allow[0].protocol` (native
  structural leaves with array indices).
- **State ops** — fixtures for a moved resource, an imported resource, and a
  forget.

Output: a new `examples/state-ops.md` golden showcasing moved/imported/forget +
nested-block, and the rich JSON/YAML small+big cases added to the existing
`big-plan.md` (or a second `examples/structured.md` if `big-plan.md` gets too
large — decide during implementation by size). All driven by `TestExamples`
goldens; the four cascade invariants must still hold (retune budgets as needed).
README "What it looks like" gains a short note + links.

## Scope / affected components

| Package | Change |
|---------|--------|
| `internal/model` | `ActionForget`, `ActionNoop`; `Change`/`Counts` gain move/import/forget fields; `AnyChange()`. |
| `internal/plan` | Parse `PreviousAddress`/`Importing`/`forget`; stop dropping them; populate annotations + counts; `RawChange` fields. |
| `internal/render` | Resource-row labels for forget/moved/imported; forget body glyph; table Stack-cell suffix; extended `changeWord`. |
| `internal/differ` | None (reuses leaf/structural/block paths). |
| `internal/fit` | None. |
| `cmd/tfstackplan` (examples) | New generators (yaml manifest, json policy, nested block, moved/imported/forget); new/expanded goldens; budget retune. |
| `README.md` | Note + links. |

## Testing

- `plan`: moved (pure + move+update), imported (pure + import+update), forget —
  assert annotations, counts, and that pure no-op/read are still dropped.
- `model`: `AnyChange()` true for move/import/forget-only.
- `render`: each row label/precedence; forget body glyph; table Stack-cell
  suffix; extended heading phrase; move-only stack renders.
- examples: golden regeneration; nested JSON/YAML small=inline vs big=folded
  (assert an array-index path appears inline; a big one folds); four cascade
  invariants hold.

## Risks

- **Budget retuning** (as before) — new content shifts sizes; invariant
  assertions catch boundary slips.
- **`forget` + `before` shape** — some providers emit sparse `before`; the
  existing `sideAttrs` already tolerates missing maps.
- **Both moved and imported on one resource** (rare) — precedence picks `moved`;
  acceptable for v1, noted.
