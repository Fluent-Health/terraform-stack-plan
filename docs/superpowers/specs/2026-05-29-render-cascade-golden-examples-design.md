# Golden example tests for the render cascade

**Date:** 2026-05-29
**Status:** Implemented

## Problem

The README shows one small rendered example. There is no committed, visible
example of how larger reports render, and no test that exercises the
`fit`/render size-budget cascade end-to-end. We want:

1. Example markdown files in `examples/` showing the different renderings.
2. Tests that validate those renderings and keep the examples honest.

Specifically, examples for: a big plan (>50 resource changes), a big plan that
pushes GitHub's comment-size limit and triggers simplifications, and one that
blasts past every simplification and is still over the limit.

## Goals

- Committed `examples/*.md` that **are** the test goldens — single source of
  truth; examples cannot drift from real tool output.
- Coverage of all four render outcomes: full, Phase-1 per-attribute
  degradation, summary-only, and the minimal best-effort floor.
- No changes to `internal/` production code; the test drives the real CLI
  pipeline.

## Non-goals

- Committing the generated `plan.json` inputs (generator is the source of truth).
- New CLI flags or rendering behaviour.
- Exhaustive matrix of every differ value-type (covered by `internal/differ`
  unit tests already).

## Design

### Architecture

A golden test in `package main` (`cmd/tfstackplan`) drives the **existing
`run(opts)`** — the production pipeline (load → gather → fit → render). Because
the examples are produced by the same code path the CLI uses, they are
guaranteed faithful.

```
genPlan(specs) -> plan.json (temp)  ┐
HCL policy (temp)                   ├─> run(opts{stacks, config, maxBytes}) -> markdown, fits
                                    ┘                                              │
                                          compare / write examples/<name>.md  <───┘
```

### Components

1. **Deterministic plan generator** — `cmd/tfstackplan/genplan_test.go`
   - `genPlan(changes ...change) []byte` marshals a Terraform-plan-shaped
     document (`format_version` + `resource_changes`).
   - Change constructors (all deterministic, no time/random):
     - `create(addr, type)` — `actions:["create"]`.
     - `del(addr, type)` — `actions:["delete"]`.
     - `replace(addr, type, before, after)` — `actions:["delete","create"]`.
     - `update(addr, type, before, after)` — `actions:["update"]` with nested
       maps (structural diff).
     - `bigUpdate(addr, type, lines)` — an update whose attribute is a large
       multi-line string, so `fit` has a large variant to degrade.
     - `iamUpdate(addr)` — an IAM resource type, for classification.
     - `sensitiveUpdate(addr)` — `before_sensitive`/`after_sensitive` set.
   - A `stackSpec{name, []change}` helper builds a stack's plan bytes.

2. **Golden runner** — `cmd/tfstackplan/examples_test.go`
   - `var update = flag.Bool("update", false, "regenerate golden examples")`.
   - For each scenario: write each stack's `plan.json` and (when classified) an
     HCL config to `t.TempDir()`, build `opts` with `--stack` refs + `--config`
     + `--max-bytes`, call `run(opts)`.
   - If `-update`: write output to `examples/<name>.md`. Else: read the golden
     and `require` byte-equality, failing with "run `go test ./cmd/tfstackplan
     -update`" guidance.
   - Each scenario additionally asserts an invariant proving its rendering (see
     table), so a wrong-mode example can't be silently blessed by `-update`.

### Scenarios

A shared input (~60 changes across ~8 stacks, including IAM, sensitive, and a
few large-diff resources) is rendered at four budgets. The classification
policy is the `examples/.tfstackplan.hcl` shape (`safe` default, `iam` preset,
`destructive` rule).

| File | Budget | Render mode | Invariant asserted |
|------|--------|-------------|--------------------|
| `examples/big-plan.md` | 60 KB (default) | Full — table + all `<details>` | contains `<details>`, no `⚠️` notice, `fits == true` |
| `examples/over-budget-degraded.md` | tight (tuned) | Full + Phase-1 attribute simplifications | contains `(hidden to fit size limit)`, `fits == true` |
| `examples/over-budget-summary-only.md` | tighter (tuned) | Summary-only — table + notice, no details | contains summary-only notice, no `<details>` |
| `examples/over-budget-minimal.md` | pathologically tiny | Minimal floor — aggregate line + notice | contains minimal notice, `fits == false` |

Exact budgets are tuned during implementation by running with `-update` and
confirming each invariant holds; they are constants in the test.

### Determinism

Generator uses index-based names and fixed content; `differ` sorts attributes;
`fit` is a deterministic stable selection. Re-running `-update` on an unchanged
tree produces byte-identical files (also what keeps CI comment upserts stable).

### README

Add a short "More examples" list under "What it looks like" linking the four
files, one line each describing the budget/outcome.

## Testing

The golden tests **are** the tests. They run in normal CI (`go test ./...`);
drift fails the build until `-update` is run intentionally. Existing
`internal/render` and `internal/fit` unit tests remain the fine-grained
coverage; these add end-to-end, human-viewable coverage of the cascade.

## Risks

- **Budget tuning brittleness.** If rendering byte sizes shift, a tuned budget
  could move a scenario across a mode boundary, failing its invariant.
  Mitigation: pick budgets with comfortable margin from each boundary;
  invariant assertions catch a boundary slip immediately and clearly.
