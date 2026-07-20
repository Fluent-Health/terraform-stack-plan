# tfstackplan — Design

**Status:** current architecture (living doc).

This document describes what `tfstackplan` **is today** — its architecture and the
enduring decisions behind it — in the present tense. It is not a changelog: the
history of how each piece was built lives in git and the PRs. Usage and knobs live
in [`docs/reference/`](reference/index.md); the *why/how* narrative for each face
lives in [`docs/guide/`](guide/02-mental-model.md). When a statement here needs a
root-cause justification, it links the PR that carries it; otherwise it states
current truth and stops.

## 1. Overview

`tfstackplan` is a single static Go binary with several **faces**, dispatched as
subcommands:

- **`render`** — a **pure renderer**. It scans a directory of per-stack
  `tfplan.json` files and emits one reviewer-friendly markdown report (plus an
  optional sidecar JSON of computed classifications). It runs no terraform and
  posts nothing. This is the standalone core and needs no server.
- **`run`** — the **CI driver**. It wraps the `terramate script run` a human would
  invoke, detects the changed stack set, runs plan/apply/verify, classifies and
  renders in-process, and reports the execution lifecycle to a `serve` control
  plane. Terraform still executes in the consumer's own CI under the consumer's
  own identity.
- **`serve`** — the **control plane**. One GitHub check run per environment,
  approval gates, apply serialization (merge-lock), and — on armed tiers — driving
  the CI runs itself from webhooks. It holds server-side state but never runs
  terraform.
- **`ui`** — the **central aggregator**. A stateless single pane of glass over
  every tier's `serve`, with in-app Google login and a PR-centric SPA.

Supporting verbs: **`state`** (operator-driven cross-stack state moves),
**`claims`** (inspect/release apply-lock claims), **`admin`**, **`catalog`**,
and **`whoami`**. See [`docs/reference/cli.md`](reference/cli.md) for the full
command surface.

The through-line: `tfstackplan` began as a pure renderer and grew a control
plane around it, but the renderer stayed pure and standalone, and Terraform
keeps executing under the consumer's own identity — the control plane observes
and gates, it never becomes the executor.

## 2. Design principles

Enduring decisions, with the rationale that still explains the system:

| Decision | Why it still holds |
|----------|--------------------|
| **Go, one static binary** | First-party `terraform-json` parsing; pure-Go SQLite (no cgo); ships as a binary *and* a distroless image from one build. |
| **Built-in renderer** (no `tfplan2md` dependency) | No external runtime in CI; fully testable offline; full control of the collapsed-`<details>` format. |
| **Input is a directory scan for `tfplan.json`** | Matches the natural output of Terramate / Terragrunt (`--json-out-dir`); no per-run manifest; the stack name *is* its directory path. |
| **Classification is git-tracked HCL policy**, in a separate file | Policy has a different lifecycle from a per-run manifest; HCL is idiomatic for the Terraform ecosystem. |
| **Presets** (built-in named rule bundles, e.g. `iam`) | Repos opt into common rulesets without rewriting regexes. |
| **Multi-label, declaration-order evaluation** | Every matching rule fires independently — a stack can carry several categories; declaration order is display order (no first-hit-wins). |
| **No user templating** | Classification is a small declarative matcher, not a free-form template surface. |
| **Functional render pipeline** (`gather → fit → render`, pure) | Build a complete budget-agnostic model, reduce it to fit, then render — each stage pure and independently testable. |
| **Structural (changed-paths / contextual) diff** for detected JSON/YAML | A readability *and* size win; a big manifest with one changed field renders as a few lines. |
| **Per-attribute `differ` overrides** | Type detection is heuristic and will misfire; an `(resource_type, attribute) → differ` override is the escape hatch. |
| **Event-sourced decider convention** (no framework) for server state | Server-side gate/execution/claim state is a pure `Decide`/`Evolve`/`React` core over an append-only event log; projections are rebuilt from the fold, never written as truth. See §6. |

## 3. `render` — the pure pipeline

A functional pipeline of pure stages around a complete intermediate `Model`. Only
the edges touch the filesystem:

```
load (I/O)  →  gather → Model (pure)  →  fit → Model' (pure)  →  render (pure)  →  write (I/O)
```

- **load** — scan `--plans-dir` for `tfplan.json` files; load the HCL config.
- **gather** — build a **budget-agnostic** `Model`: per-stack action counts, the
  matched categories, and for every changed attribute an ordered list of candidate
  render *variants* with their byte sizes.
- **fit** — pure `Fit(Model, budget) → Model'`: pick one variant per attribute so
  the estimated total fits, degrading the largest first. Touches diff depth only.
- **render** — pure `Model' → markdown`.
- **write** — stdout/file, plus the optional sidecar JSON (taken from the model
  *before* fit, since classification is never reduced).

### Package map

```
cmd/tfstackplan/   — CLI entry; orchestrate load → gather → fit → render → write
internal/domain/   — canonical value types (Counts, Category) shared across render/wire/persistence; leaf pkg
internal/plandir/  — recursively scan --plans-dir for tfplan.json; derive stack names
internal/config/   — parse + validate the HCL config (classification {} + diff {} + server/serve/ui blocks)
internal/plan/     — parse plan.json (terraform-json) → action counts + raw attr changes
internal/classify/ — resolved ordered rules → []Category; also the built-in preset bundles (iam, …)
internal/differ/   — type detect + ordered render variants per attribute
internal/model/    — Model/Stack/Change/AttrDiff/Variant (the shared spine)
internal/fit/      — pure budget reduction over the model (largest-first)
internal/render/   — Model' → markdown
```

`domain` is the single home for value types recurring across the render pipeline,
the runner→server wire protocol, and persistence (`Counts`, `Category`); `model`,
`events`, and `classify` re-export them via type aliases so each concept has one
definition, and the wire/persistence layers are codecs of it, not parallel models.

### Inputs

The renderer's only input is `--plans-dir DIR`, scanned recursively for files
named exactly `tfplan.json`. Each file is one stack; the **stack name is its
directory path** relative to the scan root (`out/platform/nonprod/tfplan.json`
→ `platform/nonprod`), sorted alphabetically, with source-aware links resolved
against `join(--repo-root, name)`. No plans found → a header-only "0 stacks
changed" report, exit zero. The full CLI surface is in
[`docs/reference/cli.md`](reference/cli.md#render); background is in
[`docs/guide/04-render.md`](guide/04-render.md).

### Plan parsing & action buckets

Each `plan.json` is parsed with `terraform-json`. For every `resource_changes`
entry, `change.actions[]` reduces to a primary action:

| `actions[]` | Bucket |
|-------------|--------|
| `["create"]` | Add |
| `["update"]` | Change |
| `["delete"]` | Destroy |
| `["create","delete"]` / `["delete","create"]` | Replace |
| `["forget"]` | Forget (removed from state) |
| `["no-op"]` + `previous_address` or `importing` | Noop (move / import only) |
| `["no-op"]` (plain) / `["read"]` | *ignored* |

A resource is additionally annotated **moved** when `previous_address` is set and
**imported** when `change.importing` is present, on top of any underlying action.
`Counts` tracks `Move`/`Import`/`Forget` alongside create/change/destroy/replace.
Any zero count column is omitted from the summary table; move/import/forget get
no columns, appearing instead as tallies on the Stack cell (`platform/prod · 1
import, 1 move, 1 forget`).

### Classification (optional)

Config is `--config FILE`, else auto-discovered `.tfstackplan.hcl` by walking **up**
to the repo root (first ancestor with `.git`). Absent → classification is disabled
and the tool degrades gracefully. The HCL schema, the deliberately small matcher
(`resource_type_pattern` / `actions` all-present / `min_count`), presets, and the
sidecar JSON shape are documented in
[`docs/reference/configuration.md`](reference/configuration.md) and
[`docs/guide/05-classification.md`](guide/05-classification.md). The enduring rules:

- Every `preset`/`rule` fires independently; a `preset` expands to its bundled
  rules at its declared position, producing the **set** of matched categories in
  declaration order (`default` is a display-only fallback, never in the sidecar or
  summary).
- **Classification considers only real-resource mutations** (add/change/destroy/
  replace) — move/import/forget make no apply-time provider write, so they need no
  elevated permission and never contribute a category, even for an `iam` rule
  ([PR #9](https://github.com/Fluent-Health/terraform-stack-plan/pull/9)).
- **Attribute recovery for gating targets**: when `emit_attributes` names an
  attribute a change doesn't carry (e.g. a net-new GCP resource's known-after-apply
  `project`), it's recovered via a per-resource `derive` block, then the stack's
  *unique* project; ambiguous stacks (zero or >1 distinct project) emit nothing —
  the per-project gate **fails closed** rather than guessing.

The sidecar's per-stack `counts` are **non-gating display data** plumbed
end-to-end as `domain.Counts`, feeding the check run's blast-radius summaries.

### The differ

Each changed attribute becomes a `model.Field`: either **leaves** (aligned `op
path = value` rows) or a foldable **block** carrying an ordered variant ladder
(preferred → minimal), each with a precomputed byte cost:

| Value | Rendering |
|-------|-----------|
| scalar / sensitive / known-after-apply | a single aligned **leaf** (`~ k = a → b`, `(sensitive value)`, `(known after apply)`) |
| JSON / YAML string, or native HCL map/list | **block:** `Structural` (contextual unified diff of the canonically re-formatted value) → `Summary` → `Hidden` |
| plain multi-line text | **block:** `LineDiff` → `Summary` → `Hidden` |
| base64 / binary | **block:** `Summary` (byte delta) → `Hidden` |

- Structured values render as a **contextual diff**, not changed-paths, so a
  policy/manifest reads naturally: the value is canonicalized (sorted keys, stable
  indent) before diffing, with line-initial `-`/`+` for GitHub's colouring.
- **Sensitivity is per-path** — the differ redacts only the sensitive leaves of
  Terraform's `before_sensitive`/`after_sensitive` tree before canonicalizing, so
  one secret field no longer smears `(sensitive value)` across an entire attribute.
- **No default per-attribute size cap** — every attribute starts at full detail and
  the global `fit` pass is the sole fit mechanism; `max_attribute_lines` is an
  optional, default-off readability ceiling.

A `diff {}` block sets defaults and can force a differ for a given
`(resource_type, attribute)` when detection misfires. The same `.tfstackplan.hcl`
also carries optional `links {}` (URL templates) and the control-plane blocks
(`server {}`, `class "<name>" {}`, `serve {}` — with its `executor "<backend>"
{}` sub-block — `ui {}`, `cache {}`) — all ignored by `render`. See
[`docs/reference/configuration.md`](reference/configuration.md).

### `fit` and the terminal cascade

`fit` starts every attribute at its preferred variant, measures the assembled
document, and while it is over `--max-bytes` advances the currently-largest
attribute one rung lossier. A stable sort (bytes desc, then stack, then address)
makes output **byte-identical across re-runs** of an unchanged plan, so CI comment
upserts don't churn. Collapsing inside `<details>` is *not* a size lever —
GitHub's 65,536-byte cap counts hidden content in full — so fitting means
actually summarizing or omitting.

When even all-minimal exceeds the budget, `fit` degrades at the report level:
**summary-only** (drop per-stack bodies, keep the table) → **minimal summary**
(one aggregate line) → **best-effort floor** (marker + minimal line, emit
regardless, exit non-zero). The marker comment is always line 1 and always
survives. `--max-bytes` defaults below GitHub's cap; `0` disables.

### Error handling

- A missing/unreadable/malformed `plan.json` fails the whole run, naming the stack
  or file — a silently dropped stack is worse than a failure.
- Invalid HCL (unknown block/field, bad regex, unknown preset) fails at config load
  with the HCL diagnostic; regexes compile once so a bad pattern fails fast.
- **Typed outcomes, not stringly errors.** The server stamps a stable
  machine-readable `code` (`internal/codes`, e.g. `GATE-001` not-classified,
  `GATE-002` not-satisfied, `GATE-003` unconfirmable) on every gate-check body;
  the runner's `GateCheck` maps it to a typed `runner.GateVerdict`, and
  `Allowed()` is true only for `Satisfied` — an unknown code or unreachable
  server **fails closed**. Unknown stack `status` values are rejected at the
  wire boundary (`events.Status` validates on decode → `WIRE-001` → HTTP 400).

## 4. `run` — the CI driver

`tfstackplan run` replaces per-stack bash glue. It wraps the same `terramate script
run` a human invokes, reports the execution lifecycle to `serve` best-effort (an
absent or unreachable server degrades to "no live progress", never build failure;
an empty server URL is a full no-op, so local runs are unaffected). Consumer wiring
is in [`docs/guide/09-ci-integration.md`](guide/09-ci-integration.md).

**Drivers.**

- **`run plan`** — detect the changed stacks, register the execution + DAG
  (`Init`), run the terramate `plan` script across the changed set, gather each
  stack's `tfplan.json`, render + classify **in-process** (reusing the render
  core), derive the approval gates and moving stacks from the classification
  sidecar, and post `Finalize`.
- **`run apply`** — over `run plan` it adds a **fail-closed gate pre-check**
  (asks serve whether the PR's gates are satisfied *before touching terramate*;
  a 409, any non-2xx, or an unreachable *configured* server blocks; an
  unconfigured server is a no-op pass), runs any pending cross-state-move
  manifests (§5 pre-phase), applies the changed stacks (terramate honours the
  dependency DAG; `--parallel N` runs independent stacks concurrently), and
  revokes the PR's grants afterward.
- **`run verify`** — runs the terramate `verify` script across changed stacks;
  no gate, read-only post-apply validation, reported under `verify/<env>`.

**Terramate exec adapter** (`internal/runner`) shells out to the `terramate`
binary: list stacks, detect the changed set, derive the dependency DAG
(`experimental run-graph` → a pure DOT parser → `events.Edge`s), and `script
run`. `runner.NormalizeEdges` suffix-matches DAG endpoints onto the listed
stacks and drops edges outside the run's set — `terramate list` yields
tier-relative paths while `run-graph` labels nodes project-root-relative, so
raw edges would otherwise dangle and the UI graph render blank.

**Lifecycle reporting verbs.**

- **`run register`** registers the full stack set up front so the check-run title
  can show `initialized k/N` / `planned k/N` against a known denominator.
- **`run wrap`** wraps a single terraform command with lifecycle ticks: a status
  before (default `running`), a derived terminal status after (`nochange` when
  terraform's summary reads `0 added, 0 changed, 0 destroyed`; `--on-success
  <status>` otherwise), and the command's output streamed live via `/api/logs`.
  It exists because terramate's parallel `script run` aborts on the first
  failing stack and never runs a *later* command, so a closing tick placed there
  would be silently skipped — putting the terminal tick inside the same command
  closes that gap. `--tty` runs it under a PTY (`creack/pty`, Unix-only) so
  terraform emits ANSI colour.
- **`run tick`** posts a best-effort per-stack `update`; **`run exec`** and the read
  verbs (`run status`/`run claims`/`run whoami`) round out the group (see
  [`docs/reference/cli.md`](reference/cli.md#run)). All are no-ops offline.

**Privilege-backed apply.** `POST /api/gate/check` returns the leased PAM
requester SA email on success. `run apply --impersonate-requester` reads it,
mints a short-lived `GOOGLE_OAUTH_ACCESS_TOKEN` via the IAM Credentials API, and
runs terraform **as the elevated requester identity** — so an unapproved IAM
change fails at GCP (403), not only at the fail-closed pre-check. A no-op when
the gate returns an empty requester (gateless plan / no server). See
[`SECURITY.md`](../SECURITY.md).

**Native provider caching.** Both drivers support a `cache {}` block that
pre-warms the local Terraform plugin cache (`TF_PLUGIN_CACHE_DIR`) from GCS
before `terraform init`, uploading newly installed providers afterward. The
warm pass **must complete before any parallel `terraform init`**: terraform's
shared plugin cache is not concurrency-safe to *populate*, so parallel inits
over a cold cache race and one stack ends up with a package whose lock hash
matches nothing (the plan-green / apply-red signature). Warm therefore runs
ahead of the classify pass and the gate pre-check (download-only, never
mutating state). Absent `cache {}` → skipped; a credential failure logs and
continues.

## 5. `state` — cross-stack move machinery

`tfstackplan state` is operator-driven state-move tooling. It writes native
Terraform declarations (or a manifest) that the normal `run apply` then applies —
serve never does state surgery. Verbs (see
[`docs/guide/08-state.md`](guide/08-state.md) and
[`docs/reference/cli.md`](reference/cli.md#state)):

- **`state move --dir DIR <from> <to> …`** routes each pair by comparing the two
  sides' stacks. All pairs validate against the relevant `tfplan.json`(s) before
  anything is written (fail-closed across both stacks). Three mechanisms:
  - **Same-stack** → a native `moved {}` block.
  - **Cross-stack** → an `import { to id }` in the destination shim + a `removed {
    from lifecycle { destroy = false } }` in the source shim — adopt into the new
    state, drop from the old, without destroying.
  - **`--via mv`** → a `_tfsp_xmove.<key>.hcl` manifest in the destination (records
    `source_stack` + the `from`/`to` intent verbatim; fan-out to concrete addresses
    happens at apply time against live state, keeping the manifest in the same
    address form as live state — [PR #159](https://github.com/Fluent-Health/terraform-stack-plan/pull/159)).

  Blocks are written to a PR-keyed shim `_tfsp_move.<key>.tf` per stack; ops
  accumulate across invocations (merged, not clobbered).
- **`state list`** lists discovered shims/manifests; **`state check`** validates all
  pending xmove manifests against the local `tfplan.json` files without running
  terraform (reports spent / valid / source-not-planned / error); **`state cleanup`**
  removes shims (`--pr` / `--all` / `--applied` for post-apply xmove GC).
- **`state apply --dir DIR [--execute] [--lock]`** runs each `_tfsp_xmove.*.hcl` via
  terraform-exec: pull both states → back up each (temp dir outside the repo, path
  printed to stderr) → per-pair fail-closed decision (source-only → move; dest-only →
  skip idempotent; both/neither → error) → `terraform state mv` on the pulled files
  → push both, never `--force`. Dry-run by default.
- **`state moves-manifest --dir DIR [-o FILE]`** emits a **two-sided** `--state-moves`
  JSON (`{"<stack>":["<addr>",…]}`) covering source move-outs *and* destination
  move-ins, in the exact shape `render/classify --state-moves` consumes — so feeding
  it back neutralizes the spurious IAM gate that would otherwise fire on the source
  stack's planned destroys ([project-management#4195](https://github.com/Fluent-Health/project-management/issues/4195)).

**`ValidateMovePlan` (fail-closed validator, `internal/statemove`).** The
canonical address namespace across generation, plan-time, and apply-time is
`prior_state` (the pre-`moved{}` snapshot in plan JSON, equal to live state at
xmove time). **Data sources are filtered out of every address walk** — they
can't be `state mv`'d; one falling under a `from` prefix emits a non-fatal
`xmove/data-source-orphan` warning. **Spent detection is per-entry**: when a
source `from` is gone from `prior_state` *and* the destination holds the `to`,
the entry emits `xmove/spent` (info) instead of a hard error, so a spent
manifest still on `main` awaiting its GC PR doesn't fail unrelated PRs' `run
plan`.

**`run apply` cross-state-move pre-phase.** After the gate check and before
terramate runs, `run apply` executes any pending manifests. Fail-closed — a
manifest that can't land cleanly aborts the apply so terramate never plans
against a half-moved state — and a no-op when none exist.

**Concurrency & rollback.** Without a lock, safety rests on terraform's
serial/lineage check on `state push` plus the backups. `--lock` adds a
**pessimistic** GCS lock via an `ifGenerationMatch=0` upload of the same
`.tflock` object terraform's GCS backend uses. If a dest `StatePush` fails
after the source push already succeeded, `Execute` **rolls the source back**
(re-pushing the pre-move state with a recovery-only `--force`). State-move
discovery is itself fail-closed: an unparseable file in the reserved
`_tfsp_move.*` / `_tfsp_xmove.*` namespace errors the read path.

## 6. `serve` — the control plane

`tfstackplan serve` is one instance per environment. It owns server-side state, one
GitHub check run per environment, approval gates, apply serialization, and — on
armed tiers — driving the CI runs from webhooks. See
[`docs/guide/07-serve.md`](guide/07-serve.md) and
[`docs/reference/install-and-deploy.md`](reference/install-and-deploy.md).

### Reconcile core

`internal/reconcile` is the pure functional core for gate/grant state:
`Decide(ChangeSet, Signal) → []Event` (all business logic, emitting past-tense
facts like `GateSatisfied`/`TargetRevoked`/`ApplySucceeded`),
`Evolve(ChangeSet, Event) → ChangeSet` (the total fold — an
empty state replayed through the log converges to the live snapshot), and
`React(ChangeSet, []Event) → []Action` (the CQRS projection deriving idempotent
shell effects: grant requests/revokes, claim releases, `RenderCheckRun`,
`PublishSSE`). The shell (`internal/server/shell.go`) replays the stream for a
`(pr, environment)`, runs `Decide`, folds with `Evolve`, persists (append →
snapshot → rebuild projections), and executes `React`'s actions serialized per
`(pr, environment)`. It is the **sole** engine — no legacy path, no flag — with
`internal/reconcile/{decide,evolve,react}_test.go` as the correctness oracle.

`GateState` is a sum type — `NotClassified` (fails closed), `Clean` (zero
targets, passes), `Pending`, `Satisfied` (all grants `ACTIVE`), `Blocked`
(denied/expired/revoked/slot-collision) — with the leased requester SA carried
inside the variant so re-plan can't clobber it; `gated`/`safe` is a **derived**
projection. The apply-time gate check is **fail-closed on an unconfirmable
reconcile** (an unresolvable PAM re-list returns `503`); otherwise it reads the
**replayed** state from the `exec:<pr>:<env>` stream: `NotClassified`/`Pending`/
`Blocked` → `409`, `Clean`/`Satisfied` → `200`.

### Store

SQLite (pure-Go `modernc.org/sqlite`, `goose` migrations via `go:embed`) holds
**three event-sourced aggregates** on a generic `internal/eventsourcing`
decider-host — one engine, three stream scopes: the **gate aggregate**
(`internal/reconcile`; the `exec:<pr>:<env>` stream; `gate_targets` is a
rebuildable cross-PR index, never read as verdict truth), the **claim-ledger
aggregate** (`internal/claims`; the `env:<env>` stream; a `ClaimSet` folded
over claim/renew/release events, where `held` is a **read-time projection** so
expiry enforces at query time), and the **execution aggregate**
(`internal/execution`; the `run:<execID>` stream; the `executions`/`stacks`/
`edges` tables plus the append-only `execution_phases` history are all
projections rebuilt from the fold, including the aggregate-owned
`superseded_by` column). Optimistic concurrency on `(stream_id, version)`
surfaces as `ErrConcurrencyConflict`.

### GitHub check runs

`RealClient` mints a short-lived App installation token per request and drives
one check run per environment (`plan/<env>` / `apply/<env>`), commit statuses,
and PR head-SHA lookups over the REST API — tested offline against an
`httptest` fake. The verdict is a **pure projection of DB state**; summaries
lead with a blast-radius headline, verdict chips, and a per-stack table. A
failed stack's error becomes an actionable triage via one bounded, ordered pure
classifier (`classifyFailure` in `internal/server/triage.go`) — an unmatched
error falls back to the raw error, never a fabricated guess.

### Approval gates

`internal/approval` is the provider-neutral gate abstraction: a `Backend`
(`RequestGrant`/`ListGrants`/`Revoke`) over a `Request{Class, Target, PR,
Environment}` and a normalised `GrantState` (AWAITING→ACTIVATING→ACTIVE, plus
DENIED/REVOKED/EXPIRED); the server only *requests*, humans approve in the
provider. Approval is **multi-class**: each `class "<name>" {}` binds to an
entitlement and an `entitlement_scope` (`projects`/`folders`/`organizations`).

`internal/approval/gcppam` is the first backend, over GCP Privileged Access
Manager: `RequestGrant` is **create-or-reuse** (reuses an open grant whose
justification matches, else creates); `ListGrants` maps PAM state →
`GrantState` and parses `(PR, environment)` back out of the justification to
correlate (fail-closed if non-round-trippable). The requester-SA pool
**leases** one identity per `(PR, environment)` — creation impersonates the
lease so PAM elevates a pool identity, not every workload; credentials are
injected for offline `httptest` testing. A `ReconcileLoop` self-heals
activating→active with no provider event; an `OrphanSweepLoop` (~5 min)
revokes grants for abandoned PRs; a merged PR's grant is released by
`ApplySucceeded`, with the PAM TTL (≤8h) as backstop.

### Apply serialization — merge-lock

serve always prevents overlapping tier-applies from colliding on per-stack
Terraform state locks. Evaluating merge safety replays the `env:<env>` stream
for the current `ClaimSet` and overlaps it with the PR's plan-time changed
stacks: empty → `clear`; overlap → `held`; indeterminable → `unverifiable`
(fails closed).

**Two webhook front-ends**, chosen by infra's governance ruleset:
**`merge_group`** (GitHub merge queue) is race-free — the queue serializes PRs
one at a time so the claim writes atomically, recovering the PR from the
merge-queue head ref since `commits/{sha}/pulls` returns `[]` for the synthetic
commit ([#221](https://github.com/Fluent-Health/terraform-stack-plan/pull/221)). **`pull_request`** posts on the PR head, re-posted at plan finalize
— simpler but has a residual race (two overlapping PRs can both see `clear`
and merge before either claim records); use the merge queue for strict
serialization.

**Claim lifecycle.** Claims attach at `Init` and stay alive via a heartbeat
lease. Release is a reconcile-core transition, not `Finalize` (which fires
mid-run, before the apply starts): the runner's apply-end `GateRevoke` maps to
`reconcile.ApplySucceeded`, emitting `ReleaseClaim` alongside the grant
revokes in one transition. A `ClaimsSweepLoop` releases expired-lease claims
and re-evaluates held checks, so a stuck check self-heals. **Admin un-wedge**:
`tfstackplan claims list` / `claims release <pr>`, or a repo admin's `success`
bypass via the Checks API.

### Consolidated `terraform/<env>` check (armed tiers)

On an **armed** tier (`runTriggerArmed()` — an `executor "<backend>" {}` block
plus `server { environment }`) a single `terraform/<env>` check per PR head
replaces the unarmed tier's `plan/<env>` + `apply-lock/<env>` surface — a
**pure render** of execution × gate × merge-lock state, recomputed on every
terminal render, with no independent posting path to fall out of sync.
Precedence:

1. Any stack failed → `failure`.
2. A rejected gate (denied/revoked/expired) → `action_required`; a gate
   merely awaiting its human keeps the check `in_progress`.
3. Still planning/applying → `in_progress`.
4. Merge-lock held/unverifiable and otherwise the sole blocker →
   `in_progress`, title `"waiting on PR #N's apply"`; clears **automatically**
   when the blocking apply releases.
5. Otherwise → `success`.

The stored gate identity never changes (`executions.status_context` stays
`plan/<env>`); unarmed tiers keep the two-check surface until each tier's
infra cutover, and post-merge `apply/<env>` is unchanged in both modes.

### Serve as the CI driver (inert until configured)

On an armed tier serve receives the GitHub webhooks and starts builds itself
instead of Cloud Build's own event triggers, so feedback appears within the
webhook turnaround. Everything is **inert** unless an `executor "<backend>" {}`
block is configured **and** the environment is known.

The run lifecycle is event-sourced: webhook → `RunRequested` → `RunQueued` →
`RunStarted` or `RunStartFailed` (never silent). A new SHA supersedes a live
plan run; a live apply is never disturbed, and execution ids are minted
deterministically (`run-<pr>-<env>-<kind>-<sha12>-a<attempt>`) since the pure
core cannot use randomness. `pull_request` drives plans, `push` to main drives
applies, and `check_run`/`check_suite` rerequested drives Re-run — the last
reaching serve only via the central UI's relay, since GitHub delivers
`rerequested` only to the App webhook. The executor seam
(`internal/executor`, `Backend{Start, Cancel, Probe}`) has one implementation,
`cloudbuild`; a `RunWatchdogLoop` fails runs stuck with no runner activity.

A build outside serve's own `StartRun` (a native check Re-run or a console
rebuild) used to strand the check. `POST /pubsub/cloud-builds`
(OIDC-verified, always ACKs) recovers the owning execution
(`_EXECUTION_ID` → `_PR_NUMBER` → `(environment, context, sha)` precedence)
and, when serve had given up on that run, emits `RunSuperseded` + `RunAdopted`
so the shell adopts the existing build without calling the executor.

### `/api` auth — Google OIDC

`/api/*` is authenticated by **verified caller identity** via **Google-signed
OIDC ID tokens** (the old HS256 shared-secret path and `internal/jwtutil` are
deleted). `serve { api_auth {} }` declares accepted token audiences and a
`principal "<email>" { scopes }` allowlist; the injectable `App.APIVerifier`
(`idtoken.Validate`) verifies the token and returns the email, and `auth`
middleware maps it to scopes — `report` (execution lifecycle, logs, gates,
claims), `read` (execution/claims reads), `admin` (claim release, future admin
verbs) — each route naming the scopes that may call it (any-of). Clients
(`internal/gauth`) obtain ID tokens from Application Default Credentials,
falling back to the IAM Credentials `generateIdToken` API as the ambient SA
(Cloud Build's metadata server has no identity endpoint). OIDC is **opt-in via
`TFSTACKPLAN_AUDIENCE`**; auth is disabled only when `api_auth {}` is
unconfigured (local/dev). See [`SECURITY.md`](../SECURITY.md).

The serve tier's own `POST /github/webhook` verifies GitHub's HMAC using
`serve.github_webhook_secret_env` and 404s when that secret is unset —
distinct from the central UI's relay secret (§7).

### The `/api` OpenAPI contract

`api/openapi.yaml` (hand-written OpenAPI 3) drives `oapi-codegen` (`go generate
./internal/api`, CI fails when out of sync): the std-http router, the models
(bound to `internal/events` via `x-go-type`), and the typed
`internal/runner.Client`. Each operation's accepted scopes are its `security`
requirement, enforced by one `apiAuth` middleware. **The contract is
cross-version** (serve/runner deploy independently, so a change is additive
only), pinned by `internal/server/testdata/wire/` golden snapshots. SSE streams
and the GitHub-webhook / Pub/Sub-push endpoints sit outside it; **read
endpoints with snake_case response bodies** were added on top: `GET
/api/executions`, `/api/approvals`, `/api/merge-queue` (degrades to empty),
and `/api/lifecycle?pr={n}`.

### Logs pipeline

`POST /api/logs` ingests per-stack output chunks into on-disk buffers under
`LogsDir` and mirrors a tail excerpt into `stack_outputs`. `GET
/logs/<exec>/<stack>` streams the buffer; `?follow=1` upgrades to SSE via a
fan-out `hub` (subscribes before replaying, so no chunk is missed). When `serve
{ objects { backend = "gcs" } }` is configured, a completed buffer offloads to
GCS and the endpoint falls back to the stored pointer. The `LogPump` tails a
per-stack `--log-file` since terramate's parallel `script run` otherwise
interleaves output onto one undemuxable stream.

## 7. `ui` — the central face

`tfstackplan ui` is the single pane of glass over every tier's serve: a **stateless
aggregator** with no domain state of its own, deployed as its own service and
configured by the top-level `ui {}` block (`tier "<name>" { url }`, `oauth {}`,
`session_secret_env`, `public_base_url`; reference in
[`examples/ui.tfstackplan.hcl`](../examples/ui.tfstackplan.hcl) and
[`docs/reference/configuration.md`](reference/configuration.md)).

- **Human auth — in-app Google OAuth** (Workspace-internal client, no IAP/LB):
  authorization-code flow with `openid email profile`, the Workspace domain
  enforced against the **verified** id_token's `hd` claim (a consumer account →
  403). The session is an **AES-256-GCM encrypted cookie** (key = SHA-256 of the
  configured secret, so rotating it invalidates every session) holding identity
  only — the SPA never sees Google tokens.
- **Service auth toward the tiers — Google OIDC** (`gauth.Source` per tier
  audience): the UI's SA needs a `read`-scoped principal in each tier's
  `api_auth {}`.
- **Contract-first JSON API** — `api/ui.openapi.yaml` → `internal/uiapi` (the
  SPA's TypeScript types come from the same document): `/api/me`, `/api/tiers`,
  and tier-scoped proxies bound to `internal/api` via `x-go-type`, relaying
  response bodies **verbatim**; a dead tier is a `502` naming it. Session-authed
  streaming proxies (outside the contract) cover the tier's SSE change stream,
  log reads (`?follow=1` with `Last-Event-ID` resume), and the rendered plan
  HTML fragment, which the SPA injects rather than re-implementing.
- **GitHub App webhook relay** (`POST /github/webhook`): the single ingress for
  App-scoped deliveries (the Re-run buttons), forwarded **verbatim** to every
  tier's `/github/webhook`, so each serve verifies GitHub's HMAC end-to-end.
  `ui.github_webhook_secret_env` optionally makes the relay verify too
  (defense-in-depth) — a *separate* field from `serve.github_webhook_secret_env`
  (§6).
- **The SPA** (`web/ui/`: SolidJS + TypeScript + Vite + Tailwind/daisyUI) is
  **PR-centric** — the unit of work is a PR, which *contains* its tiers. Three
  surfaces behind a nav rail: the **PRs** landing list, the **Ops board**
  (applier-slot panels per tier, errored runs, awaiting-approval), and the **PR
  view** hero (both tiers side by side, each with a **unified lifecycle
  stepper** — colour-only segments, one per lifecycle *phase* folded from the
  tier's executions against a fixed template, plus a stage badge, tagline,
  component groups, a gates strip, and a collapsible dependency graph). Live
  updates are **reload-free**: a payload-less `changed` stream debounces into a
  refetch and Solid patches only what moved.
- **In-UI PAM approve/deny** — stateless incremental-consent: `GET
  /auth/approve` seals the intent into the OAuth `state` and requests
  `cloud-platform` with `include_granted_scopes` (one consent, silent popups
  after); the callback spends the user's short-lived token on the single PAM
  `:approve`/`:deny` call and discards it — no user credential is ever stored,
  and the PAM audit log records the human. The reason is required (PAM rejects
  empty). **Decisions push, not poll**: a successful call fires the tier's
  `POST /api/gate/reconcile`, and the reconcile's render publishes on the SSE
  stream within seconds.

## 8. `uniqueness` — cross-env config lint

A second, independent lint (its own subcommand, `tfstackplan uniqueness`) that
catches per-environment config values that were copy-pasted instead of
parameterized. It scans a directory of per-stack instance manifests (the
Catalyst `BundleInstance` YAML shape by default) rather than plan JSON, and has
its own `env_uniqueness {}` config block, its own report shape, and its own
exit-code contract — it shares only the `.tfstackplan.hcl` file and its
auto-discovery with `classification {}`/`diff {}`.

### What it checks

Two detectors, run per unit (one unit = one instance manifest, with one set of
inputs per environment):

- **Cross-env identifier duplicates** — the same value appears, unchanged, in
  ≥2 of a unit's environments, and the value is identifier-shaped. A leaf is
  identifier-shaped by **value-shape** (matches a built-in pattern: UUID,
  `https?://…` URL, dotted hostname+TLD, or a long opaque numeric string) OR by
  **key-pattern** (its key's final dot-segment ends `_id`, `_uuid`,
  `client_id`, `project_id`, `domain_id`, `account_id`, …) — either is
  sufficient; a bare enum, boolean, or semver-looking string matches neither.
  Two exclusions keep this from firing on legitimate repetition:
  - **Quantity suffixes** (`_ms`, `_seconds`, `_count`, `_port`, `_percent`,
    `_timeout`, …) are never identifiers, even when the value is a long
    number (e.g. a `jwt_expiration_ms` shared by design).
  - **Env-scoped paths** — a dot-path with any segment matching a known
    environment name, its derived token, or a configured extra segment — are
    skipped entirely, since they're expected to enumerate every env (e.g. a
    map keyed by environment name).
- **Env-token cross-check** — a leaf value in one environment's inputs that
  embeds *another* environment's identifying token (its derived project name
  or bare env name) — e.g. a `dev` URL hardcoded into `prod`'s config. Matched
  as a whole token, not a substring (RE2 has no lookbehind, so the boundary is
  consumed as a literal non-alphanumeric character or start/end-of-string
  rather than asserted), so `acme-dev` doesn't false-positive inside
  `acme-development`.

### Tier-aware severity

Every environment belongs to a **tier** (its protection class, e.g. `prod` vs.
`nonprod`). Severity for a cross-env duplicate depends on which tiers its envs
span:

- **`VIOLATION` (blocking)** — the duplicate's envs include both the
  `protected_tier` and some other tier (e.g. one client secret shared between
  `dev` and `prod`).
- **`REPORT-ONLY` (non-blocking)** — the duplicate is confined to envs within
  a single tier (e.g. shared only between `dev` and `test`, both `nonprod`) —
  surfaced in the report but never fails the command.
- An environment **missing from the tier map fails closed**: it's always
  treated as `protected_tier`, never as an escape hatch.
- **Env-token findings are always `VIOLATION`**, regardless of tier — a leaked
  token is a mistake independent of which tiers are involved.

### Config: `env_uniqueness {}`

A block in the same auto-discovered `.tfstackplan.hcl` used by
`classification {}`/`diff {}`:

```hcl
env_uniqueness {
  protected_tier         = "prod"          # optional; default "prod"
  project_token_template = "acme-{env}"    # optional; derives per-env tokens + env-scoped path segments

  # Env topology: EITHER declare each env's tier explicitly...
  environment "dev"  { tier = "nonprod" }
  environment "prod" { tier = "prod" }
  # ...OR read it from a data leaf instead — set source.tier_input below and
  # drop the environment {} blocks; when tier_input is set it always wins and
  # that leaf is excluded from the detectors themselves.

  source {
    # Catalyst BundleInstance defaults shown; override only for another layout.
    # glob              = "components/**/instances/*.tm.yml"
    # environments_path = "environments"
    # inputs_path       = "inputs"
    # tier_input        = "tier_class"
  }

  extra_env_tokens    = { dev = ["acme-development"] }  # optional, per-env extra tokens
  extra_scoped_segments = ["region"]                    # optional extra env-scoped path segments
  extra_key_patterns    = ["_secret_ref$"]              # optional extra identifier key regexes

  allow {
    unit    = "svc/api/instances/api"       # exact unit id
    key     = "inputs.shared_client_id"     # exact key, or an fnmatch glob
    envs    = ["dev", "test"]               # must be a superset of the finding's envs
    reason  = "shared sandbox OAuth client, ticket ACME-123"  # required
    expires = "2026-12-31"                  # optional, YYYY-MM-DD
  }
}
```

- **`source`** controls discovery and shape: `glob` is a
  `<dir>/**/<pattern>` path (Go's `filepath.Glob` has no `**`, so this walks
  the tree and matches prefix/suffix manually); `environments_path` navigates
  each manifest to its per-env map; `inputs_path` navigates each environment
  to its inputs, which are then flattened to dot-paths. All three default to
  the Catalyst shape (`environments.<env>.inputs.*`).
- **`allow {}`** is the inline justification for a reviewed, intentional
  duplicate. It's non-exclusive with severity: only `VIOLATION`-severity
  findings need justification (report-only findings never need one and are
  never "unjustified"). `key` matches exactly or as an `fnmatch`
  (`filepath.Match`) glob; `envs` must be a superset of the finding's envs;
  `reason` is mandatory (config fails to load without it). An expired `allow`
  (past its `expires` date) is dropped before matching, so the violation it
  used to cover resurfaces as unjustified; an `allow` that matches nothing
  live is reported as **stale**.

### Command

```
tfstackplan uniqueness [--dir DIR] [--config FILE] [--format text|json]
```

- `--dir` (default `.`) — repo root scanned for instance manifests.
- `--config` — same auto-discovery as `render`'s `--config` (falls back to
  `.tfstackplan.hcl`, found by walking up to the repo's `.git` root); the
  resolved file must contain an `env_uniqueness {}` block.
- `--format` — `text` (grouped human-readable list) or `json` (the report,
  machine-readable).
- Exit codes: **0** clean; **1** one or more unjustified violations or stale
  allow rules; **2** usage/config/load error (bad flags, no config found, HCL
  decode error, undeclared or inconsistent environment tier, invalid
  `extra_key_patterns` regex, unparsable source manifest). Report-only
  findings never affect the exit code.

### Known limitations

- **Identifier detection is a value-shape/key-pattern heuristic, not a
  schema.** A field that's really an identifier but matches none of the
  built-in value patterns or key suffixes goes undetected unless the repo
  adds an `extra_key_patterns` entry — or, once it's flagged some other way,
  a reviewed `allow` records the exception.
- **A duplicate wholly within unprotected tiers is report-only by design** —
  the lint's blocking concern is the protected/unprotected boundary, not
  general no-duplication across nonprod environments.
- **Env-scoped fan-out paths are skipped structurally, not merely
  downgraded** — a key whose path includes an env name/token/configured
  segment is assumed to be legitimate per-env enumeration and never appears
  in either detector's output.
- **Single protected-tier boundary model.** One designated `protected_tier`
  versus everything else; there's no N-tier crossing matrix (e.g. no
  distinct rule for `staging → prod` versus `dev → staging`).
- **The env-token fold is a deliberate superset of the env-token detector.**
  A value equal to another environment's derived token also classifies as
  identifier-shaped, so the duplicate detector may flag it too (in addition
  to `FindEnvTokens`); a single `allow{}` covers both findings.


## 9. Delivery

One codebase ships **two artifacts**: the standalone binary (Homebrew/Docker) and a
multi-arch (`linux/amd64`+`linux/arm64`) **distroless Cloud Run image** whose
entrypoint is the same binary's `serve`. Because the binary is fully static
(pure-Go SQLite, no cgo) and embeds its assets (migrations, built SPA) via
`go:embed`, `go build` alone always works and the image needs no runtime
files. The release GitHub Action builds the SPA once and overwrites
`internal/ui/dist/` before each `go build`, so CI stays node-free while the
committed placeholder keeps the tree buildable. Deployment notes are in
[`docs/reference/install-and-deploy.md`](reference/install-and-deploy.md);
hardening is in [`SECURITY.md`](../SECURITY.md).

## 10. Testing strategy

- **The server clock is injectable** (`App.now func() time.Time`), the single
  seam for wall-clock reads; tests reassign it to drive expiry
  deterministically, no `time.Sleep`.
- **Render pipeline** — table tests: `plan` per action bucket; `classify` over
  resolved `[]Rule` + synthetic changes; `config` parse fixtures + each error
  case; `differ` per type (variant ladder, cap on/off); `fit` over synthetic
  models (largest-first order, byte-identical determinism, every cascade
  rung); `render` golden markdown.
- **Decider tests** (`internal/reconcile/{decide,evolve,react}_test.go`) are
  the oracle for every gate/execution/claim transition.
- **Offline fakes** keep the whole server e2e-testable with no credentials or
  network — a GitHub `httptest` fake, an `httptest` PAM fake, a fabricated
  Google issuer (`gauth/gauthtest`: a throwaway RSA key drives the *real*
  client-library minting + verification loop), and a vendored real-terramate
  fixture (skips cleanly when terramate isn't installed).
- **Wire goldens** pin the exact bytes of every `/api` response across
  versions. **gofmt CI gate**: the test job fails on any non-gofmt'd file
  (`gofmt -l .` catches what `vet` does not).

## 11. Known limitations / gotchas

**Render / classification**

- **`--plans-dir` couples a stack's name to its directory** (name = dir relative
  to the scan root; links resolve against `repo-root/name`), so the plans tree
  must mirror the stack tree. Decoupling is intentionally unsupported.
- **The canonical plan filename is hardcoded `tfplan.json`** (matches
  Terragrunt's `--json-out-dir`; Terramate is scripted to emit the same). A
  `tfplan.json` at the scan root errors (no stack name).
- **A *changed* sensitive leaf inside a structured value shows no visible diff
  line** — per-path redaction marks it `(sensitive value)` on both sides
  (deliberate — never leak the value). **Nested *known-after-apply* still
  collapses to the whole attribute**, since that treatment wasn't extended to
  `after_unknown`; coarser, not a leak, and deferred.
- **A `move`/`import`/`forget` of an IAM resource is not flagged by the `iam`
  guard** — classification is mutation-only, so these need no grant. The
  notable case is `forget` (`removed {}`), which drops a resource from state
  while leaving the live binding — real drift the guard intentionally does not
  surface.

**Run / logs**

- **PTY mode is Unix-only and merges stdout+stderr.** `run wrap --tty` uses
  `creack/pty` (needed for terraform to emit ANSI colour); on Windows or any
  allocation failure it falls back to the pipe path (logs one line, command
  still runs, exit code preserved).

**Serve / state / approvals**

- **The server's SQLite store is single-writer by design** (one instance per
  environment); WAL + `busy_timeout` are set at the DSN level so every pooled
  connection retries instead of returning `SQLITE_BUSY`.
- **A grant that expires or a PR that closes *after* the apply gate-check
  returns 200 is enforced by GCP IAM, not the gate-check** — the 200 grants no
  privilege of its own, so a revoked grant fails the apply partway with no
  unauthorized writes. The one residual gap is GCP's own **IAM-propagation
  delay** (~seconds to a few minutes) after a revocation. Separately, **GCP PAM
  exposes no per-grant deep link** — reviewers land at the project-scoped
  "Pending approval" tab and locate their grant by PR/env.
- **Merge-lock races depend on the chosen front-end** (`merge_group` is
  race-free, `pull_request`+auto-merge has a residual race); a long apply also
  holds its claim for the full duration (**head-of-line blocking**), and the
  lease window must exceed the apply heartbeat gap or the sweep can release a
  live apply's claim.
- **Exec executions store no per-stack plan diff**, so the SPA's Plan tab is
  empty for apply stacks — populated only by the plan driver's
  `Finalize.StackReports`.
- **Execution/stack/edge status is event-sourced through the `internal/execution`
  aggregate** (the per-execution `run:<execID>` stream on the shared decider host):
  `handleInit`/`handlePhase`/`handleUpdate`/finalize drive it via `HandleExec`, and
  `executions`/`stacks`/`edges` (+ the `execution_phases` history) are true
  projections rebuilt from the fold — the source-of-truth invariant now holds for
  execution state, not just gate/claim state. `superseded_by` is likewise
  aggregate-owned: both supersede triggers (the Runs-map `CancelRun` path and
  `handleInit`'s direction-guarded `FindNonSupersededExecution`) emit a `Superseded`
  fact on the *old* execution's stream, and the projection folds it — there is no
  direct `superseded_by` write. The remaining carve-out stays written directly (not
  owned by the aggregate): `report_markdown`/`change_reasons` (finalize
  presentation), `check_run_id` (a GitHub side-effect id), and `created_at` (reset on
  a reviving Init inside the projection's `ON CONFLICT` — it needs a wall-clock, which
  the pure core has no access to).
- **The gate overlay and the runner-told status share the `stacks.status` column**,
  reconciled at the projection layer (the gate projection overlays `gated`/`safe`;
  `projectExecution` writes the runner status; the overlay skips `failed`/`aborted`).
  In the **normal flow** this is coherent and the plan-gate `gated` display is stable
  (no exec signals arrive after a plan finalizes). Two residual, **apply-context-only
  and self-healing** deltas exist: (a) because `projectExecution` reprojects *all*
  stacks' status on every exec signal, a gate-target stack not yet ticked can
  transiently read its runner status instead of `safe` when a sibling stack ticks;
  (b) a failed apply's per-tick `ReportFail` marks still-in-flight siblings `aborted`
  until each sends its own completion tick. Both are cosmetic, resting-state-correct,
  and re-asserted by the next reconcile-loop gate tick. The clean fix (decouple the
  runner status from the gate-overlay display, e.g. read-time derivation) is a
  future refinement.
- **The merge-queue hero depends on GitHub-App token visibility of
  `mergeQueue`** — repo/permission-dependent; every layer degrades to an empty
  queue and the hero hides rather than erroring.
- **A console rebuild of an already-green SHA flips `terraform/<env>` back to
  in-progress** (user-visible but intentional), and **SHA-based inbound-build
  correlation assumes plan head SHAs are PR-unique** —
  `_EXECUTION_ID`/`_PR_NUMBER` precedence exists to avoid relying on this.

## 12. Future / deferred

- `tfplan2md` shell-out renderer behind a `--render` flag; additional presets
  (`data`, `cluster`, `schema-migration`).
- Multi-part output (split into several comments) as an alternative to the
  terminal cascade's truncation.
- Run-to-run diffing (highlight what changed since the last report).
- Multi-tier in one comment; SARIF / static-analysis rollup.
