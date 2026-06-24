# Examples

Real rendered output, not mock-ups. Every `*.md` file here is a **golden** — it
is produced by the renderer itself and checked by the test suite, so it always
reflects how `tfstackplan render` actually behaves today.

## Rendered comments

All of these are rendered from one shared input (56 changes across 8 stacks —
IAM, sensitive, destructive, structural, and large-diff resources) under one
classification policy ([`.tfstackplan.hcl`](.tfstackplan.hcl): a `✅ safe`
default, an `🔐 iam` preset, and a `💣 destructive` rule). What differs between
files is the **byte budget**, which drives how aggressively the renderer folds:

| File | What it shows |
|------|---------------|
| [`big-plan.md`](big-plan.md) | The whole plan inside a comfortable 60 KB budget — per-stack `<details>`, full diffs, no folding cascade. The "everything fits" baseline. |
| [`over-budget-degraded.md`](over-budget-degraded.md) | A tighter budget: the renderer starts collapsing large rows to stay under the limit, but keeps the structure. |
| [`over-budget-summary-only.md`](over-budget-summary-only.md) | Tighter still: bodies drop away and the comment degrades toward the summary table. |
| [`over-budget-minimal.md`](over-budget-minimal.md) | The floor — what survives under an extreme budget. |
| [`long-names.md`](long-names.md) | `for_each` member keys (including the empty key `name[""]`) in row summaries, and a monospaced import id on the descriptor line. |
| [`state-ops.md`](state-ops.md) | State operations — moved (↪️), imported (📥), forgotten (⏏️) — alongside structured-value contextual diffs (`(json)` / `(yaml)`) and a nested-block diff. |

The byte-budget cascade is explained in
[the classification chapter](../docs/guide/05-classification.md#the-byte-budget);
how the comment is laid out is in [the render chapter](../docs/guide/04-render.md).

## Example configs

| File | What it is |
|------|------------|
| [`.tfstackplan.hcl`](.tfstackplan.hcl) | The classification policy used for the rendered examples above — a good starting point for your own. |
| [`serve.tfstackplan.hcl`](serve.tfstackplan.hcl) | A `serve` control-plane config (GitHub App, approval, groups). See [Configuration](../docs/reference/configuration.md). |

## Regenerating the goldens

The `*.md` files are not hand-edited. They're regenerated from the renderer:

```bash
go test ./cmd/tfstackplan -update
```

Running `go test ./cmd/tfstackplan` (without `-update`) checks the committed
goldens still match the renderer — so if a render change alters output, the test
fails until you regenerate and review the diff. The scenarios live in
[`cmd/tfstackplan/examples_test.go`](../cmd/tfstackplan/examples_test.go).
