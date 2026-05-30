# Render an empty (0-stacks) report instead of erroring

**Date:** 2026-05-29
**Status:** Approved (design)

## Problem

`tfstackplan` rejects zero-stack input: with no `--stack`/`--manifest` it prints
`no stacks: pass --manifest or --stack`, and an explicit empty manifest
(`stacks: []`) fails in `manifest.Load` with `manifest <path>: no stacks`. Both
exit non-zero and produce no output.

A CI consumer (the Fluent Health `infra` plan trigger) wants to post **one PR
comment per tier per PR even when no stacks changed**, so that the *presence* of
a comment is a positive "the plan ran" signal and its *absence* flags that the
trigger didn't run. The comment is upserted by its environment-encoding marker
(`<!-- tf-plan:<tier> -->`), which `tfstackplan` emits. Today the consumer can't
get that comment from the tool for the no-change case because the tool errors.

## Goal

Let an **explicit empty manifest** (`stacks: []`) be valid input that renders a
minimal report and exits 0:

```
<!-- tf-plan:nonprod -->
### Terraform plan — nonprod  (0 stacks changed)
```

…and emits `{}` for `--emit-classification-json`. Passing **neither**
`--manifest` nor `--stack` still errors — empty output is only reachable by
explicitly handing over an empty manifest, so a caller that simply forgot to
pass stacks is still caught.

Non-goals: changing the rendered shape for non-empty plans; a CLI flag for
emptiness (the empty manifest *is* the opt-in); any change to classification,
diffing, links, or the byte budget.

## Decisions (locked)

- **Opt-in = explicit empty manifest.** `stacks: []` is valid; neither-flag
  remains an error. No new flag.
- **Empty body = heading only.** Marker + `### <title>  (0 stacks changed)`,
  with the count folded into the existing heading style. No summary table, no
  details, no `Class` column. Header links (build/PR) still render if
  configured — they're run-level and useful on the no-change comment too.
- **Sidecar = `{}`** when `--emit-classification-json` is set.

## Design

### Components

| File | Change |
|------|--------|
| `internal/manifest/manifest.go` | Remove the `len(m.Stacks) == 0 → error` guard in `Load`. An empty `stacks` list returns a `Manifest` with empty `Stacks` and no error. |
| `internal/render/render.go` | In `Render`, after writing the marker line, if `len(r.Stacks) == 0`: call `renderHeader` (which already prints `(0 stacks changed)` via `changedStacks`) + the header-links block, then return — skip `renderTable` and `renderDetails`. Applies to the normal and summary-only modes; minimal mode is untouched. |
| `cmd/tfstackplan/main.go` | No change. The `--manifest` branch already sets `refs = m.Stacks` (empty); the `default` (neither-flag) branch still errors. The per-stack gather loop runs zero times; `report.Stacks` is empty; the existing `if o.classJSON != "" && classified` block marshals the empty sidecar map to `{}`. |

### Control flow (Render)

```
write "<!-- marker -->"
if len(r.Stacks) == 0:            # new short-circuit (normal + summary modes)
    renderHeader(b, r)            # "### <title>  (0 stacks changed)" + header links
    return
# …existing table + details path unchanged…
```

`changedStacks(r)` already returns 0 for an empty report, so the heading reads
`(0 stacks changed)` with no special-casing of the count. Pluralisation is left
as "stacks" (matches the existing format string; "0 stacks" is correct English).

### Sidecar

With zero stacks the gather loop adds nothing to the `sidecar` map, so
`json.MarshalIndent(sidecar, …)` produces `{}`. No code change; covered by a
test.

## Testing

- `internal/manifest`: an empty `stacks: []` manifest `Load`s without error and
  yields `len(Stacks) == 0` (replaces the current "empty → error" expectation).
- `internal/render`: a `model.Report` with no `Stacks` renders the marker + the
  `(0 stacks changed)` heading and contains **no** `| Stack |` table delimiter;
  with `HeaderLinks` set, the links line is present.
- `cmd/tfstackplan`: an empty-manifest run with a classification config →
  returns no error and `fits == true`; stdout begins with the marker and
  contains `(0 stacks changed)` and no table; the `--emit-classification-json`
  file contents equal `{}`.

## Risks

- **Relaxing `manifest.Load` is global** — any empty manifest is now valid, not
  just the CI's. Acceptable: it's an explicit caller choice, and the neither-flag
  guard in `main.go` still catches accidental no-input. Documented here.
- **Header-only output must not emit a stray table** — the short-circuit returns
  before `renderTable`, so the empty case never prints a header-only table. The
  render test asserts the absence of the table delimiter.

## Downstream consumer (separate `infra` change, after v0.4.1)

The plan trigger's zero-changed-stacks early-exit writes a tiny manifest
(`{title: "Terraform plan — <tier>", marker: "tf-plan:<tier>", stacks: []}`) and
runs `tfstackplan --manifest … --emit-classification-json classes.json --output
comment.md`, then upserts the comment as usual — so every tier always leaves its
marker-keyed comment. Pairs with a `gh_upsert_comment` marker-idempotency fix
(don't double-prepend a marker the body already carries). Ship as tool **v0.4.1**
and bump the `TFSP_VER` pin in the trigger.
