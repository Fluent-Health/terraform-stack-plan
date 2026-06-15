# CLAUDE.md — terraform-stack-plan

`tfstackplan` renders many Terraform `plan.json` files (one per stack) into a
single reviewer-friendly markdown PR comment, with optional classification.
The living design doc is **`docs/DESIGN.md`**.

## Superpowers doc convention (overrides skill defaults)

Instruction priority: these rules win over the superpowers skills' defaults.

- **Specs + plans are gitignored scratch.** The brainstorming spec and the
  implementation plan are local working artifacts under `docs/superpowers/`
  (gitignored). Never commit them; never treat their absence as a problem or
  re-create them to "restore" history.
- **Rationale lives in the PR description.** When opening the PR, put the spec's
  essence in the body — summary, key decisions (and the alternatives weighed),
  and risks/limitations. The PR is the durable record of *why* and *how*.
- **Keep the living doc current.** Before opening the PR, update
  `docs/DESIGN.md` to reflect the change: revise the relevant sections —
  including **Known limitations / gotchas** — to *current truth*, concisely,
  linking the PR for the full reasoning. Capture what is true now and what to
  watch out for; do **not** paste the spec or the why/how journey.

## Server-side state (reconciler core)

All server-side gate/execution state transitions go through `internal/reconcile`'s
pure `Step(World, Signal) → (ChangeSet, []Action)`. The imperative shell
(`internal/server/shell.go`) gathers a scoped `World`, calls `Step`, executes the
returned `Action`s, and persists the new `ChangeSet` — serialized per
`(pr, environment)`. Never mutate gate/execution state inline in a handler.

Add new behavior as a `Step` transition with a row in the permutation harness
(`internal/reconcile/step_table_test.go`), not as a new handler branch. The
engine is gated behind the off-by-default `reconciler_core` serve flag (engaged
only at quiescence; see `serve --check-quiescent`); the legacy gate handlers
remain as the flag-OFF path until a post-cutover cleanup.
