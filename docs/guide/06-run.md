# 06 — `run`

> CI logs are a wall of text that nobody reads until something is already on fire.

The observability gap closes when `run` and [`serve`](07-serve.md) work
together: `run` drives your stacks and **reports** every lifecycle event —
which stack started, finished, failed, changed nothing — and `serve` turns that
stream into a live DAG view, per-stack streamed logs, and check runs. `run` is
the reporter; `serve` is the viewer. Without `run` feeding it, an apply is a
black box with a spinner. With it, it's a thing you can watch.

## What `run` actually is

`run` is not a replacement for `terramate script run`. It is a wrapper around
it. The same scripts you'd type at a laptop run inside `run` — which means the
same terraform commands, the same identities, the same dependency ordering. What
`run` adds is everything that happens *around* those commands:

- **Change detection.** `run plan` asks terramate which stacks changed relative
  to a base ref, so CI plans only what the PR touched.
- **Execution registration.** `run plan` registers the execution — stacks,
  dependency DAG, metadata — with the control plane (`serve`) before the first
  terraform command runs. That's what produces the check run and the live DAG
  before you'd otherwise know whether anything will succeed.
- **In-process render + classify.** When planning finishes, `run plan` gathers
  every stack's `tfplan.json`, runs the same render + classification pipeline
  that `render` does, and finalizes the report with the server. There is no
  second tool invocation. The comment that lands on the PR is produced the same
  way regardless of whether you call `render` standalone or `run plan` in CI.
- **Per-stack progress.** The terramate scripts call `run tick` and `run step`
  between commands. Each call is a lightweight server post — a status tick, a
  log chunk — that the live viewer reflects immediately.
- **Gate enforcement.** `run apply` is the one place in the whole system that
  is deliberately fail-closed: it calls the server's gate pre-check *before
  touching terramate*, and blocks if the gates are not satisfied.

An absent or unreachable server degrades the build to "no live progress, no live
UI" — never to failure. The gate pre-check is the single exception to that rule.

## The subcommands

### `run plan`

```
tfstackplan run plan --dir DIR
        [--changed]          # default true — plan only changed stacks
        [--parallel N]       # parallel plan jobs (0 = terramate default)
        [--base REF]         # git base ref for change detection
        [--script NAME]      # terramate script to run (default "plan")
        [--log-file NAME]    # per-stack log file the script tees (default tfstackplan.log)
        [--config FILE]      # default: auto-discover .tfstackplan.hcl under --dir
```

The plan driver. It:

1. Detects which stacks changed (by default, relative to the merge base).
2. Calls `Init` on the server — registers the stacks, their dependency DAG, and
   the execution id. The check run appears here, before a single plan has run.
3. Runs the terramate `plan` script across the changed set, in parallel up to
   `--parallel`. Each stack's script calls `run step`/`run tick` to stream
   progress; `run plan` sets the `TFSTACKPLAN_*` environment so they know where
   to report.
4. Gathers each stack's `tfplan.json` (written to `<stack>/tfplan.json` by the
   script), renders and classifies in-process, and derives the approval gates
   (each gating `class` paired with its emitted target values).
5. Posts `Finalize` — the rendered comment, the classification JSON, the gate
   targets, and the list of stacks with pending state moves.

The rendered report is also printed to stdout, so `run plan` is useful even
with no server configured: the comment lands on stdout and you post it however
your CI posts comments.

### `run apply`

```
tfstackplan run apply --dir DIR
        [--changed] [--base REF] [--script NAME]   # default script "apply"
        [--log-file NAME]
        [--parallel N]
        [--state-lock]           # pessimistic GCS lock around cross-state moves
        [--impersonate-requester]
```

The apply driver, and the one `run` subcommand that is fail-closed by design.
Its phases, in order:

1. **Gate pre-check.** Before terramate is invoked, `run apply` posts to
   `/api/gate/check`. A 409, any non-2xx, or a timeout on a *configured* server
   blocks the apply completely — no stack is touched. An unconfigured server
   (`TFSTACKPLAN_SERVER` unset) is a no-op pass.
2. **Cross-state move pre-phase.** Executes any pending `_tfsp_xmove.*.hcl`
   manifests (written by `state move`). `--state-lock` wraps this phase in a
   pessimistic GCS lock. Also fail-closed.
3. **Apply.** Runs the terramate `apply` script across the changed stacks in
   dependency order. Independent stacks run concurrently up to `--parallel N`
   (default: serial). Each stack reports its progress via `run step`/`run tick`.
4. **Grant revocation.** After the apply completes, revokes the PR's PAM grants
   (best-effort — a revocation failure does not fail the build).

`--impersonate-requester` is the privilege-backed deployment mode: `run apply`
takes the requester service-account email the server returns in the gate-check
response and mints a short-lived `GOOGLE_OAUTH_ACCESS_TOKEN`, so subsequent
terraform invocations run as the leased PAM identity rather than the ambient CI
identity. The full wiring — PAM entitlement, requester pool, ADC requirements —
is in [09 — CI integration](09-ci-integration.md).

### `run verify`

```
tfstackplan run verify --dir DIR [--changed] [--base REF] [--script NAME] [--log-file NAME]
```

Runs the terramate `verify` script (by default) across the changed stacks — a
read-only, post-apply validation pass. There is no gate pre-check here: `run
verify` never blocks. It reports its own `verify/<env>` check run, with its own
live page and a per-stack Verify tab. Use it for drift detection, policy checks,
or any validation that makes sense after an apply has settled.

### `run tick`

```
tfstackplan run tick [--stack PATH] [--status STATUS] [--detail TEXT]
```

The per-stack progress reporter. Your terramate scripts call this between
commands to tell the server what a stack is doing right now. `run tick` reads
the execution context from the `TFSTACKPLAN_*` environment, posts a best-effort
update, and exits zero regardless of the server's response. It is a complete
no-op offline. A tick never fails the build.

In practice you will more often reach for `run step`, which wraps a single
terraform command and handles the before/after ticks automatically — including
detecting `nochange` from terraform's output and streaming logs to the server as
the command runs. The CI integration chapter covers when to use each, and why
wrapping every terraform command in `run step` (rather than calling `run tick`
in a separate command) closes a gap in how terramate's parallel `script run`
handles failures. See [09 — CI integration](09-ci-integration.md).

## How `run` relates to the other faces

`run` sits between `render` and `serve` in the flow:

- It produces **the same comment** `render` produces — same code path, same
  byte budget, same classification output. The only difference is that `render`
  is invoked standalone against a pre-existing directory of `plan.json` files,
  while `run plan` collects those files itself and renders in-process.
- It feeds **`serve`'s live view and check runs** by reporting each lifecycle
  event — `Init`, per-stack ticks, `Finalize`. Remove the server from the
  picture and `run` still works; it just prints the report and exits.

The boundary is deliberate: terraform keeps running in your CI, under your
identities. `serve` observes and gates; `run` is the reporter. Neither takes
over the actual execution.

## Environment variables

`run` reads its server context entirely from the environment — nothing is
threaded through the terramate scripts as flags:

| Variable | Meaning |
|---|---|
| `TFSTACKPLAN_SERVER` | Control-plane base URL. Empty → fully offline: `run tick` no-ops, the gate check passes, the report goes to stdout. |
| `TFSTACKPLAN_AUDIENCE` | OIDC audience (the serve URL) for `/api/*` auth via Google ADC — the only `/api/*` credential (the legacy shared token was removed). |
| `TFSTACKPLAN_ENVIRONMENT` | Deployment environment (e.g. `staging`, `prod`). |
| `TFSTACKPLAN_EXECUTION` | Execution id. `run plan`/`run apply` generate one if unset; export it so per-stack `run tick` reports under the same id. |
| `TFSTACKPLAN_PR` | PR number — correlates plan and apply gate checks. |
| `TFSTACKPLAN_REPO` | `owner/repo` — used for check runs and commit statuses. |
| `TFSTACKPLAN_SHA` | Head commit SHA. |
| `TFSTACKPLAN_STACK` | Current stack path (fallback for `run tick --stack`). |

The full flag reference is in [Reference → CLI](../reference/cli.md). The full
CI wiring — GitHub Actions YAML, Terramate scripts, `run phase` for early
check-run appearance, `run step` vs `run tick` — is in
[09 — CI integration](09-ci-integration.md).

---

Next: [07 — `serve` →](07-serve.md)
