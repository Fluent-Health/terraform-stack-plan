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

## Server-side state (event-sourced reconciler core)

All server-side gate/execution state transitions go through `internal/reconcile`'s
pure decider: `Decide(ChangeSet, Signal) → []Event` (domain facts), `Evolve(ChangeSet,
Event) → ChangeSet` (the fold), and `React(ChangeSet, []Event) → []Action` (effects).
The imperative shell (`internal/server/shell.go`) reconstructs the scoped `ChangeSet`
by replaying the event stream for a `(pr, environment)`, runs `Decide`, folds the new
events with `Evolve`, persists (appends events + snapshot + rebuilds projections), then
executes the `React` actions — serialized per `(pr, environment)`. The event log is the
source of truth; projections (e.g. `gate_targets`) are rebuilt from the fold, never
written directly. Never mutate gate/execution state inline in a handler.

Add new behavior as decider transitions — a new `Event` emitted by `Decide`, folded in
`Evolve`, surfaced as an `Action` in `React` — covered by the decider tests
(`internal/reconcile/decide_test.go`, `evolve_test.go`, `react_test.go`), not as a new
handler branch. The reconcile core is the sole gate/execution engine — there is no
legacy path or feature flag (the `reconciler_core`/`apply_lock`/`use_checks` flags and
the quiescence cutover tooling were removed post-cutover; the pre-split `Step` engine
was retired in PR 1 of the docs-restructure/cleanup effort).
