# `--plans-dir`: a single, convention-based input surface

**Date:** 2026-05-30
**Status:** Approved (design)
**Background:** [docs/terramate-integration.md](../../terramate-integration.md) —
Terramate/Terragrunt research that motivated this shape.

## Problem

tfstackplan's input is assembled by the caller: today via a YAML/JSON
`--manifest` (title + marker + an explicit `name`/`plan`/`dir` list) or repeated
`--stack NAME:PATH` flags. In practice the plans come from a multi-stack
orchestrator (Terramate, Terragrunt) that runs `terraform show -json` per stack.
That leaves every caller hand-writing manifest-assembly glue, and the tool
carries three input concepts (`--manifest`, `--stack`, and the `manifest`
package) for what is really one job: "here are N plans, render them."

Research (see background doc) established:

- Terramate has **no plugin SDK** and **no cross-stack reduce** — its `script`
  runs per-stack, in each stack's directory. The rollup is exactly the gap
  tfstackplan fills.
- A **self-describing output directory** is the cleanest contract: each per-stack
  command independently drops its plan into a central tree, so the directory's
  contents *are* the stack set — no aggregation step needed to build the input.
- This is **orchestrator-neutral**: Terragrunt's native `--json-out-dir` already
  produces `<dir>/<unit-path>/tfplan.json`; Terramate is scripted to match.

## Goal

Replace all existing input modes with one convention-based directory scan:

```bash
tfstackplan --plans-dir out/ --config .tfstackplan.hcl --output report.md
```

where `out/` mirrors the stack tree and each stack's plan is at
`out/<stack>/tfplan.json`. The directory is the single, inspectable, uploadable
input artifact for a run.

## Decisions (locked)

- **Single input mode.** `--plans-dir DIR` is the only way to supply stacks.
  **Remove `--manifest`, `--stack`, and the entire `internal/manifest` package.**
- **Canonical plan filename: `tfplan.json`, hardcoded** (no `--plan-file` flag).
  It is Terragrunt's native `--json-out-dir` output name, and Terramate is
  scripted to write the same — one name serves both tools with zero config.
- **Discovery is a recursive scan.** Find every `tfplan.json` under `DIR`; each
  one's containing directory, relative to `DIR`, is the **stack name**
  (depth-agnostic: `out/platform/nonprod/tfplan.json` → `platform/nonprod`).
- **Source dir = `join(--repo-root, <stack name>)`.** The mirrored-tree
  convention means the stack name *is* its source path from the repo root, so
  source-aware links resolve `.tf` files there with no extra config. A
  non-mirrored `out/` simply doesn't resolve resource links (falls back to the
  stack link) — documented, not an error.
- **Ordering: lexicographic by stack name.** Deterministic; the tool does not
  consume any orchestrator's run-order.
- **Title/marker come from `--title`/`--marker` flags only.**
- **Empty/missing semantics:**
  - `--plans-dir` **absent** → error (`no input: pass --plans-dir`). Presence of
    the flag is the opt-in; forgetting it is still caught.
  - `--plans-dir` points at a **nonexistent path** → error.
  - dir exists but contains **no `tfplan.json`** → valid **0-stack report**
    (reuses the shipped 0.4.1 empty-report path: heading + `{}` sidecar).
- **No change to** classification, diffing, source-aware links, the sidecar, the
  byte-budget/`fit` cascade, or the rendered markdown shape. This feature only
  changes how `report.Stacks` and each stack's source dir are populated.

## Design

### Components

| File | Change |
|------|--------|
| `internal/manifest/` | **Deleted.** `Manifest`, `StackRef`, `Load`, `ParseStackFlags` all go. A small `StackRef{Name, Plan, Dir}` struct (or equivalent) moves to where discovery lives (`internal/plandir` or `cmd`). |
| `internal/plandir/plandir.go` (new) | `Scan(dir string) ([]StackRef, error)` — walks `dir`, collects every `tfplan.json`, derives `Name` = parent dir relative to `dir` (forward-slash), `Plan` = full path to the file. Returns refs sorted lexicographically by `Name`. Errors if `dir` does not exist; returns empty slice (no error) if it exists with no matches. |
| `cmd/tfstackplan/main.go` | Replace the `--manifest`/`--stack` flags and the manifest/stack-flag branches in `run` with a single `--plans-dir` flag. `run` calls `plandir.Scan`, then sets each stack's source dir to `filepath.Join(repoRoot, ref.Name)` (replacing the old `ref.Dir`/`filepath.Dir(ref.Plan)` default) — that dir feeds `source.Build` and the `{stack_dir}` link var resolves to `ref.Name`. Title/marker no longer read from a manifest. The `default`-no-input branch becomes "`--plans-dir` required". |
| `cmd/tfstackplan/*_test.go` | Drop `--stack`/`--manifest` cases; add `--plans-dir` e2e cases (see Testing). Regenerate any golden examples that invoked the old flags. |
| `README.md`, `docs/DESIGN.md` | Rewrite the Usage/Inputs/CLI sections around `--plans-dir`; add an orchestrator recipes section (below). Update the `## CLI reference` block. |

### CLI surface (after)

```
tfstackplan --plans-dir DIR
            [--title TEXT] [--marker TEXT]
            [--config FILE]                 # HCL policy; default: auto-discover .tfstackplan.hcl
            [--max-bytes N]                 # default 60000; 0 disables
            [--details auto|open|closed]    # default closed
            [--emit-classification-json FILE]
            [--repo-root DIR]               # base for link file paths (default ".")
            [--link-var key=value]          # repeatable
            [--output FILE | -]             # default '-' (stdout)
            [--version]
```

### Control flow (`run`)

```
if plansDir == "":           error "no input: pass --plans-dir"
refs = plandir.Scan(plansDir)   # errors if dir missing; empty slice if no plans
for ref in refs:                # zero iterations → empty report (0.4.1 path)
    parse plan; classify; diff; build links
    stackDir = join(repoRoot, ref.Name)     # source dir for links
    ...
render
```

### Orchestrator recipes (README)

**Terramate** — per-stack `script` writes the plan into the central tree using
`terramate.stack.path.to_root` / `…path.relative`, then one render step:

```hcl
script "plan-report" {
  job {
    commands = [
      ["terraform", "plan", "-out", "tfplan.bin"],
      ["sh", "-c", "mkdir -p ${terramate.stack.path.to_root}/out/${terramate.stack.path.relative} && terraform show -json tfplan.bin > ${terramate.stack.path.to_root}/out/${terramate.stack.path.relative}/tfplan.json"],
    ]
  }
}
```
```bash
terramate script run plan-report
tfstackplan --plans-dir out/ --config .tfstackplan.hcl --output report.md
```

**Terragrunt** — native `--json-out-dir` already produces the right shape:

```bash
terragrunt run --all --filter-affected plan --json-out-dir out
tfstackplan --plans-dir out --config .tfstackplan.hcl --output report.md
```

## Testing

- `internal/plandir`: nested `tfplan.json` files → correct names (relative,
  forward-slash, depth-agnostic) and lexicographic order; non-`tfplan.json`
  files ignored; nonexistent dir → error; existing empty dir → empty slice, no
  error.
- `cmd/tfstackplan` (e2e):
  - a `--plans-dir` tree with a classification config → expected report + sidecar.
  - empty dir → marker + `(0 stacks changed)` heading, no table, sidecar `{}`,
    exit 0, `fits == true`.
  - missing `--plans-dir` → error; nonexistent dir → error.
  - source-aware links resolve against `join(repo-root, name)` for a mirrored
    tree (extend the existing links e2e to the new input mode).
- Remove `examples_test.go`/`main_test.go` assertions tied to `--stack`/`--manifest`.

## Risks / trade-offs

- **Breaking change.** `--manifest`/`--stack` removal breaks any current caller.
  Mitigation: pre-1.0; the **0.4.1 empty-report consumer** (infra plan trigger)
  migrates from writing a `stacks: []` manifest to `mkdir -p out && tfstackplan
  --plans-dir out …` — a net simplification. Ship as a minor bump with a
  CHANGELOG note; bump the `TFSP_VER` pin in the trigger in the same change.
- **Lost capability: `name ≠ source-dir`.** The convention forces
  `name == path == source-dir`. Acceptable: the caller generates `out/` and can
  always mirror the tree; both target tools do. If a real need appears, a future
  manifest-style escape hatch can return — explicitly out of scope now.
- **Empty `out/` looks like a clean PR.** A mis-targeted plan-write yields an
  empty dir → "0 stacks changed", indistinguishable from a genuinely no-op PR.
  Consistent with the 0.4.1 philosophy; detecting "did the plan step run" is the
  CI step's job (its exit code), not tfstackplan's.
- **Hardcoded `tfplan.json`.** A tool that emits a different name needs a rename
  step. Accepted for surface cleanliness; revisit only if a third orchestrator
  with an unchangeable, different name shows up.

## Out of scope

- `--stacks-from` / stdin list input (superseded by the directory scan).
- Any orchestrator-specific code in tfstackplan — it stays a pure consumer; the
  glue is the documented per-tool recipe.
- Re-introducing per-stack `dir`/title/marker overrides.
