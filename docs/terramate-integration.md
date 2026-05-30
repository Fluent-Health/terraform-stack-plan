# tfstackplan × Terramate — Integration Findings

**Version:** 0.5 (draft)
**Status:** Findings — not yet a committed design
**Date:** 2026-05-30
**Source basis:** Terramate `terramate-io/terramate` @ `436aa7e9` (2026-05-27), plus
[terramate.io/docs](https://terramate.io/docs). All code citations below are
`file:line` in that revision.

---

## Purpose

tfstackplan's input today is a hand-assembled manifest (or repeated `--stack`
flags). In practice the plans it consumes are produced by **Terramate**
orchestrating `terraform plan` / `terraform show -json` across changed stacks.
This doc records what Terramate *actually* exposes (verified against its source,
not just docs) and what that means for the cleanest way to wire the two together.

The headline conclusion: **tfstackplan cannot and should not become a Terramate
plugin; it should become the cross-stack *reduce* step that Terramate
structurally lacks, fed directly from Terramate's own change-detection output.**

---

## Source-verified findings

### 1. No third-party extension/plugin SDK

Terramate has no plugin system, no way to register custom subcommands, and no
public hook API. The only "hooks" in the tree are **internal Go function types
compiled into the binary**, wired exclusively to cloud sync:

- `engine/run.go:106` — `RunBeforeHook`
- `engine/run.go:109` — `RunAfterHook`
- `ui/tui/cli.go:80` — `PostInitEngineHook`

Using any of these requires forking and vendoring the engine. **The only
supported integration model is "wrap it" — `terramate run` / Terramate Scripts /
code generation.**

### 2. `terramate list` emits newline-delimited paths only (no JSON)

`commands/stack/list/list.go`:

- `:118-128` — prints `friendlyDir` per stack, one per line; with `--why` prints
  `"<dir> - <reason>"`.
- `:21-32` — the command `Spec` struct has **no format/JSON field**. There is no
  machine-readable output; stdout lines are the contract.
- `--changed` change detection and `--run-order` ordering are supported and
  reflected in that stdout order.

Implication: consuming `terramate list --changed` over a pipe is the correct,
stable seam. With `--why`, lines carry a ` - <reason>` suffix to account for.

### 3. `terramate run` exposes no per-stack metadata in the environment

`run/env.go:44` (`LoadEnv`) builds the child process environment from **only**:

- `os.Environ()` (inherited), plus
- the `terramate.config.run.env {}` block, evaluated per-stack
  (`:54-56` set the `terramate` / `global` / `env` eval namespaces; `:104-109`
  emit the final `KEY=value` pairs).

There is **no automatic `TM_STACK_PATH` / `TM_STACK_NAME` / `TM_STACK_ID`
injection.** A wrapped tool cannot "read the current stack from its environment"
unless the user explicitly defines those vars in `run.env`.

Implication: don't rely on env for stack identity. A pipe (stdin) is the right
boundary.

### 4. Terramate Scripts are strictly per-stack — there is no reduce step

`commands/script/run/run.go:174-211`: a script run expands to a **flat list of
`StackRun` tasks** over `(script × stack × job × command)`. Jobs/commands run
once *per stack*. There is no "after all stacks" / aggregate / reduce construct
anywhere in the script model.

Implication: tfstackplan's whole job — merging N plans into one report — **cannot
be expressed as a Terramate script**. It must run as a separate step *after* the
per-stack fan-out.

### 5. Plan previews are a per-stack → cloud feature; no local rollup exists

`commands/script/run/run.go:212-230` attaches `CloudSyncPreview` / `CloudPlanFile`
per-stack-task; rendering happens server-side (`cloud/client_preview.go`,
`cloudsync/create_preview.go`).

- **Terramate Cloud** posts rendered PR plan previews (destructive-change
  highlighting, policies, approvals) — but this is **cloud-only**.
- The **free/OSS** path is a plain ASCII plan dump posted via a generic
  PR-comment Action, **truncated at GitHub's 65,536-byte comment cap**.

Implication: there is **no local, OSS, multi-stack rendered rollup** in
Terramate. That is exactly tfstackplan's niche, and its byte-budget degradation
is the direct answer to the 64 KB truncation the free path suffers.

### 6. Code generation can emit a manifest, but paths-only (weak)

`generate_file { context = root }` produces one file at the project root with
access to `terramate.root.*` and `terramate.stacks.*`. However
`terramate.stacks.list` is a **list of stack path strings only** — root context
does **not** expose per-stack `name` / `id` / `tags` / `description` (those live
in `terramate.stack.*`, which is stack-context only). So a generated manifest
could carry paths but not rich metadata. This route is strictly worse than
piping `terramate list` and is not recommended.

---

## What this means for tfstackplan's shape

| Concern | Owner |
|---|---|
| Which stacks exist / changed | **Terramate** (`list --changed`, `run --changed`) |
| Per-stack fan-out, ordering, parallelism | **Terramate** (`run --parallel`, `after`/`before`) |
| Per-stack `plan → show -json` | **Terramate** (a `script` job) |
| **Cross-stack rollup into one artifact** | **tfstackplan** (nobody else) |

Terramate does **expansion**; it never does **reduction**. tfstackplan is the
reduce. This is the strongest argument for keeping it a separate tool and
*not* trying to fold it into Terramate.

It also revises an earlier idea (a filesystem `--plans-glob`): a glob re-derives
from disk what Terramate already knows authoritatively (which stacks changed,
their canonical names, their order) and risks picking up stale `plan.json` from
unchanged stacks. **Driving discovery off Terramate's own output is strictly
better.**

---

## Recommended integration shape (proposed, not yet specced)

A Terramate-native input mode, additive to the existing `--manifest` path:

```bash
terramate list --changed \
  | tfstackplan --stacks-from - \
                --plan '{stack}/out/plan.json' \
                --config .tfstackplan.hcl \
                --output report.md
```

- `--stacks-from -` reads newline-delimited stack paths from stdin — Terramate's
  native `list` output. Stack name = the path Terramate reports.
- `--plan PATTERN` resolves each stack's plan file via a `{stack}` template, so
  there is no manifest to assemble.
- Change detection, identity, and ordering all come from the orchestrator that
  owns them; the pipe is the integration.

The end-to-end CI story, matching Terramate's grain:

1. **Per-stack `script` job** (shipped as a documented recipe):
   `terraform plan -out plan.tfplan` → `terraform show -json plan.tfplan > out/plan.json`.
2. **`terramate list --changed`** — the expansion / change set.
3. **`tfstackplan --stacks-from -`** — the reduce Terramate can't do.

`--manifest` stays for non-Terramate users and runs needing per-stack `dir` /
title / marker overrides. `--stack NAME:PATH` becomes redundant under this mode
and is a retirement candidate.

---

## Open questions / decisions for the spec

1. **`--why` tolerance:** does `--stacks-from -` accept bare paths only, or also
   parse/strip the ` - <reason>` suffix that `terramate list --why` appends?
2. **`{stack}` template vocabulary:** what placeholders does `--plan PATTERN`
   support — just `{stack}` (the path), or also `{stack_abs}`, basename, etc.?
   How does this interact with the existing link-template var set?
3. **Missing plan files:** if a listed stack has no plan at the resolved path
   (e.g. listed-but-not-planned), is that an error, a skip, or a "no changes"
   row? Terramate's changed set and the planned set can differ.
4. **Stack naming:** Terramate `list` prints working-dir-relative friendly dirs;
   confirm these are the names we want in the report (vs. `terramate.stack.name`,
   which `list` does not emit).
5. **Positioning:** should the README explicitly frame tfstackplan as the OSS,
   no-64 KB-cliff alternative to Terramate Cloud previews (finding #5)?

---

## Next step

Take `--stacks-from -` / `--plan PATTERN` (the Terramate-native input mode)
through brainstorm → spec. It is the single highest-leverage change these
findings point to and is purely additive to the existing manifest path.
