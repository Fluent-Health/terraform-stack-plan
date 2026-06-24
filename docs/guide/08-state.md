# 08 — `state`

> Running `terraform state mv` against prod by hand is a perfectly reasonable
> thing to do. Once.

[Chapter 01](01-the-gaps.md) named the management gap: cross-stack refactors
— splitting a stack, pulling a bucket into its own home, renaming a module
— require hand-running `terraform state mv` against a live backend, outside
the normal plan/apply cycle, with nothing reviewable on the PR. `state` closes
that gap. You declare the moves in your repo; they apply as part of the normal
`run apply`; and they surface in the rendered comment so reviewers can see
exactly what moved and where.

## The core idea

When you run `tfstackplan state move`, the tool writes shim files directly
into the affected stack directories. Those shims are checked in alongside your
Terraform code and travel through the same PR → plan → approve → apply path
everything else does. At apply time, `run apply` picks them up automatically —
no separate step, no manual intervention.

The moves appear in the rendered PR comment as state-op rows alongside the
normal resource changes: ↪️ for a moved resource, 📥 for an import, and ⏏️
for a resource dropped from state without being destroyed. See
[`04-render.md`](04-render.md) for how those rows look and
[`examples/state-ops.md`](../../examples/state-ops.md) for a worked output.

Crucially, **state operations never contribute a classification category**.
A pure relocation doesn't trip the `🔐 iam` or `💣 destructive` gate — it's
treated as bookkeeping, not a provider write.

## Declaring moves

```
tfstackplan state move --dir DIR [--stack STACK] [--pr N] [--via mv] \
    <from> <to> …
```

Each `<from> <to>` pair is an address (e.g.
`google_storage_bucket.legacy_assets` → `google_storage_bucket.assets`).
`--stack` is the default stack for unqualified addresses; you can also write
`stack:addr` inline to route each side explicitly.

Before writing anything, `state move` validates every pair against the
relevant `tfplan.json`(s) — if any pair fails validation, nothing is written
(fail-closed across both stacks). The routing logic then splits each pair by
whether its sides land in the same stack or different ones:

- **Same-stack** — emits a native `moved {}` block in the stack's shim.
- **Cross-stack** — emits an `import { to … id … }` block in the destination
  shim and a `removed { … lifecycle { destroy = false } }` block in the source
  shim. The `id` for the import is read from the `before.id` field in the
  source plan, so the resource is adopted into the new state and dropped from
  the old without being destroyed.
- **`--via mv`** (cross-stack only) — instead writes a `_tfsp_xmove.<key>.hcl`
  manifest in the destination stack, executed by `state apply` (and by the
  `run apply` pre-phase) via `terraform state mv` rather than through the
  native `import`/`removed` mechanism.

Shims are keyed by PR (`PR-<n>` from `--pr` or `$TFSTACKPLAN_PR`), then by
branch (`branch-<name>`), then `local`. Multiple `state move` invocations
accumulate into the same shim — existing blocks are merged, not clobbered.

## The other subcommands

```
tfstackplan state list    --dir DIR [--pr N]
tfstackplan state cleanup --dir DIR (--pr N | --all)
tfstackplan state apply   --dir DIR [--execute] [--lock]
tfstackplan state moves-manifest --dir DIR [--pr N] [-o FILE]
```

**`state list`** shows every discovered shim under `--dir` — the key, the
stack, and a human-readable op line (`moved from → to`, `import to (id=…)`,
`removed from`). Useful for a quick audit before applying.

**`state cleanup`** removes the keyed shim files — either one PR's shims or
every `_tfsp_move.*` shim in the tree. Run it after a PR merges; CI can wire
it up automatically.

**`state apply`** is the `--via mv` executor. It discovers every
`_tfsp_xmove.*.hcl` manifest under `--dir`, then for each pair: pulls both
states, backs them up under `.tfsp-state-backups`, checks which side already
holds the resource (idempotent if it's already at the destination, error if
it's in neither), and runs `terraform state mv` against the pulled local
files. It never passes `--force` on forward pushes. Dry-run by default;
`--execute` performs the actual moves. `--lock` acquires the pessimistic GCS
lock before each move (see [Concurrency](#concurrency) below). Requires
`terraform` on `PATH`.

**`state moves-manifest`** scans all shims and xmove manifests under `--dir`
and emits a two-sided JSON file: source move-outs (the planned destroys on the
source stack) and destination move-ins (the planned creates on the destination
stack). Feed this to `render --state-moves moves.json` so the classifier
recognises both sides as relocations rather than real destroys or creates. The
typical CI wiring is:

```bash
tfstackplan state moves-manifest --dir . -o moves.json
tfstackplan render --plans-dir out/ --state-moves moves.json …
```

## Cross-state moves and IAM

When a resource moves between stacks, the source stack's plan contains a
**destroy** and the destination stack's plan contains a **create**. Without the
`--state-moves` overlay, both sides look like real resource mutations — and an
IAM resource relocation would fire the `🔐 iam` gate on the source side's
planned destroy.

`state moves-manifest` generates the overlay that tells `render`/`classify`
which planned creates and destroys are relocations. With the overlay in place,
the source stack's move-out and the destination stack's move-in are both marked
as state operations and excluded from gate-triggering classification.

For the detailed mechanics — the two-sided JSON shape, the `Covers` address
matching, and what happens when live plan instances drift from the recorded
addresses — see [`../DESIGN.md`](../DESIGN.md) (the `tfstackplan state`
section).

## How `run apply` picks up the moves

You don't need to run `state apply` manually in the normal flow. `run apply`
has a **cross-state move pre-phase**: after the fail-closed gate check and
before `terramate` starts, it executes any pending `_tfsp_xmove.*.hcl`
manifests using the same executor as `state apply`. If a manifest cannot land
cleanly, the pre-phase aborts the apply (exit 1) so Terramate never plans
against a half-moved state. When no manifests exist — the common case — this
phase is a complete no-op. Pass `--state-lock` to `run apply` to wrap the
pre-phase moves in the pessimistic GCS lock.

## Concurrency

Without `--lock`, safety rests on Terraform's built-in serial/lineage check
on `state push` — an optimistic check — plus the pre-move backups. With
`--lock` (or `--state-lock` on `run apply`), the tool acquires the GCS
`.tflock` object before each move using an `ifGenerationMatch=0` upload — the
same object the GCS backend uses for its own locking — so a concurrent
`terraform` operation fails to lock and the move fails before touching state.
The lock is fail-fast: an already-held lock errors out rather than waiting.
The backend bucket and prefix are read from the stack's `*.tf` file
(`terraform { backend "gcs" { bucket prefix } }`).

## Failure and rollback

If a cross-state move's destination `StatePush` fails after the source push
already succeeded, `state apply` rolls the source back by re-pushing the
in-memory pre-move state. The rollback push uses `--force` (recovery only);
the forward pushes never do. If even the rollback fails, the error message
points at `.tfsp-state-backups` for manual restore.

Discovery is fail-closed: a shim file in the `_tfsp_move.*` /
`_tfsp_xmove.*` namespace that cannot be parsed errors the read path rather
than being silently skipped. A silently dropped manifest would let a
relocation classify and apply as a real destroy-and-create.

## Reference

All flags are documented in [`../reference/cli.md`](../reference/cli.md). The
worked rendered output — with ↪️ moved, 📥 imported, and ⏏️ forgotten rows
— is in [`../../examples/state-ops.md`](../../examples/state-ops.md).

---

Next: [09 — CI integration →](09-ci-integration.md)
