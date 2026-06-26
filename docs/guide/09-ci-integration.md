# 09 — CI integration

> One checkout, one `run` invocation per phase. Everything else is glue
> you no longer need to write.

This is the wiring chapter. It assumes you have read [06 — `run`](06-run.md)
(what the subcommands do) and [07 — `serve`](07-serve.md) (what the control
plane does). Here you will find the concrete pieces: Terramate scripts,
environment variables, GitHub Actions YAML, check-run behaviour, privilege-
backed deployment wiring, and the edges worth knowing about.

## The pieces

```
PR opened ──▶ CI plan job ──▶ tfstackplan run plan  ──┐
                                                       ├─▶ tfstackplan serve  ──▶ GitHub check run + live UI
merge     ──▶ CI apply job ─▶ tfstackplan run apply ──┘       (control plane)        + approval gates
                  └─ each invokes `terramate script run …` (the same scripts a human runs)
```

- **`run plan`** — detects the changed stacks (`terramate list --changed`),
  registers the execution and DAG with the server, runs the terramate `plan`
  script across the changed set (in parallel up to `--parallel N`), gathers
  each stack's `tfplan.json`, renders and classifies in-process, and finalizes
  the report and approval gates.
- **`run apply`** — runs a fail-closed gate pre-check (refuses to apply unless
  the server says the PR's gates are approved), then applies the changed stacks
  (terramate honours the dependency DAG; `--parallel N` runs independent stacks
  concurrently, default 0 = serial), then revokes the grants.
- **`run phase`** — emits a lifecycle phase event (`warming`, `initializing`,
  `planning`) so the check run appears early, before the first plan completes.
- **`run step`** — wraps a single terraform command with lifecycle ticks:
  ticks `running` before, determines the terminal status after (`safe` /
  `nochange` / `failed`), and streams the command's output to the server as
  log chunks. Transparent offline passthrough — a no-op tick when
  `TFSTACKPLAN_SERVER` is unset, so local `terramate script run` is unaffected.
  Prefer `run step` over separate `run tick` pairs for the apply command — see
  *Terramate scripts* below.
- **`run tick`** — the per-stack progress reporter the Terramate scripts call
  directly when `run step` is not the right fit. A no-op offline; never fails
  the build.

## Environment

The `run` commands read their context from the environment, so nothing is
threaded through the Terramate scripts as flags:

| Variable | Meaning |
|---|---|
| `TFSTACKPLAN_SERVER` | Control-plane base URL. **Empty ⇒ fully offline** — `run tick` no-ops, the apply gate check passes, the report goes to stdout. |
| `TFSTACKPLAN_TOKEN` | Bearer secret for the server's `/api/*`. |
| `TFSTACKPLAN_ENVIRONMENT` | Deployment environment (e.g. `staging`, `prod`). |
| `TFSTACKPLAN_EXECUTION` | Execution id. Set it to correlate plan and apply of the same PR; if unset, `run plan`/`run apply` generate one. Export it so the script's `run tick` reports under the same id. |
| `TFSTACKPLAN_REPO` | `owner/name` — used to build the check run and commit status. |
| `TFSTACKPLAN_SHA` | Head commit SHA. |
| `TFSTACKPLAN_PR` | PR number — correlates plan and apply gate checks. |

A down or absent server degrades the build to "no live progress" — never to
failure — **except** the apply gate pre-check, which is deliberately
fail-closed. Full variable reference is in
[Reference → Environment](../reference/environment.md).

## Terramate scripts

The consumer defines `plan` and `apply` scripts once (root `scripts.tm.hcl`,
inherited by every stack). They run terraform via `run step`, which wraps each
terraform command — the runner invokes these exact scripts, so there is one
execution code path. Enable scripts in the root config and pass the per-stack
path via Terramate's stack metadata:

```hcl
terramate {
  config {
    experiments = ["scripts"]
  }
}

script "plan" {
  description = "init + plan, capture the plan JSON, report progress"
  job {
    commands = [
      ["tfstackplan", "run", "step", "--stack", "${terramate.stack.path.relative}", "--", "terraform", "init", "-input=false", "-lock=false"],
      ["tfstackplan", "run", "step", "--stack", "${terramate.stack.path.relative}", "--on-success", "planned", "--", "terraform", "plan", "-input=false", "-lock=false", "-out=plan.bin"],
      # Emit the plan JSON where `run plan` gathers it: <stack>/tfplan.json.
      ["tfstackplan", "run", "step", "--stack", "${terramate.stack.path.relative}", "--", "sh", "-c", "terraform show -json plan.bin > tfplan.json"],
    ]
  }
}

script "apply" {
  description = "init + apply, report progress"
  job {
    commands = [
      ["tfstackplan", "run", "step", "--stack", "${terramate.stack.path.relative}", "--", "terraform", "init", "-input=false"],
      ["tfstackplan", "run", "step", "--stack", "${terramate.stack.path.relative}", "--on-success", "safe", "--", "terraform", "apply", "-input=false", "-auto-approve", "plan.bin"],
    ]
  }
}
```

Notes on the scripts:

- `run plan` gathers each stack's `tfplan.json` from the stack directory
  (`<stack>/tfplan.json`), so the `show -json` step must write there.
- `run tick` and `run step` are no-ops when `TFSTACKPLAN_SERVER` is unset and
  never fail the build, so these scripts run unchanged at a laptop.
- **Wrap every terraform command in `run step`** (both plan and apply).
  Terramate's parallel `script run` aborts on the first failing stack and never
  advances to a *later* command in the same job — so a closing
  `run tick --status planned`/`safe` in a separate command is silently never run
  when an earlier stack fails. Wrapping the terminal command in
  `run step --on-success planned` (plan) / `--on-success safe` (apply) puts the
  terminal tick *inside* the same command Terramate runs to completion, closing
  that gap. `run step` also streams the command's output to the server as log
  chunks, superseding the older `tee` + `--log-file` pump.
- `run step` auto-detects `nochange` from terraform's summary line
  (`Apply complete! Resources: 0 added, 0 changed, 0 destroyed`) and ticks that
  status instead of `safe`. The `--on-success safe` flag sets the terminal status
  for any other (non-zero-change) success.
- **`--tty` enables ANSI colour in the viewer.** Terraform suppresses colour when
  its stdout is a pipe. Add `--tty` to any `run step` command to run terraform
  under a PTY so it emits ANSI colour; the live viewer renders it automatically
  (including progress spinners). Also drop `-no-color` from the terraform flags —
  both changes together produce colour. If PTY allocation fails (Unix-only),
  `run step` falls back to the normal pipe path — the command still runs and exits
  with the correct code; one line is logged to note the fallback.
- The `--stack` flag is taken verbatim. If your terraform root is deeper than the
  Terramate stack path, wrap with a one-line `bash -c` to strip the key prefix.
- `run step` streams logs itself — the old `tee tfstackplan.log` convention and
  the `--log-file` pump are superseded for `run step` stacks. (The runner still
  starts the pump; with every command wrapped in `run step` it finds no log file
  to tail and no-ops, so there is no double-streaming.)

## Early start and progress

To surface the check run before planning completes, the CI plan job emits
`run phase` events before and after long-running steps. Pin a stable execution
id (`TFSTACKPLAN_EXECUTION=$BUILD_ID` in the job env) so all events correlate
to the same run. Emit `--phase warming` before the plugin cache warm and
`--phase initializing` before the sequential terraform init; the server creates
the per-environment check run on the first phase event. The check-run title and
summary headline carry a phase-weighted progress bar (⠿⠀ Braille cells,
`warming → initializing → planning k/N → done`), updated in real time.

The same early-phase sequence applies to **apply**: emit `--phase warming` and
`--phase initializing` before the respective pre-apply steps, then the stack
apply progress follows. Set `TFSTACKPLAN_EXECUTION=$BUILD_ID` in the CI apply
job so all phase and tick events share one execution id. Without this the apply
check run will not appear until the first `run tick` fires from within a stack.

## Check-run title: progress bar vs action-count summary

While a run is **in-progress** the per-environment check title shows a live
progress bar (`Plan · ⠿⠿⠿⠿⠇⠀⠀⠀⠀⠀ · 3/5 stacks`). Once the run is **terminal**
(all stacks done, or plan/apply exited) the title switches to an action-count
summary:

- Plan: `Plan · +6 ~3 −2 · 12 stacks` or `Plan · no changes · 12 stacks`
- Apply: `Apply · applied 12/12`

The bar only renders while the run is alive. A finished apply is marked
terminal, the live viewer stops streaming, and the frozen-bar problem from
earlier versions is gone.

## Cross-state moves: streaming to the stack log

Cross-state move output (the lines `terraform state mv` emits for each address)
is streamed to the per-stack log via the same sink used for plan/apply output.
The viewer log pane therefore shows move progress in real time alongside the
stack's other output — no separate move-specific route.

## Live viewer behaviour

- The stack detail panel opens on the **Log tab while the stack is streaming**
  and switches to the **Result tab once the stack is done** (terminal status).
- The log pane **tail-follows** new output by default; scrolling up pauses
  following (stick-unless-scrolled-up), so an operator can read without the
  view jumping.

## Failure detail backfill

When `run apply` fails, stacks that never reached a terminal tick are marked
`aborted` (not `failed`) by the server's finalize sweep — the distinction
reflects that these stacks were not attempted, not that they errored. Stacks
that did run and failed emit a `failed` tick. In both cases, the server reads
each stack's stored log excerpt and backfills the failure detail from the last
terraform diagnostic block (`╷ … ╵`), falling back to the last error line or
final N lines if no diagnostic is found. A stack with no captured logs still
shows the "see the build log" note.

## Apply identity and privilege-backed deployment

### Advisory mode (default)

By default `run apply` uses the ambient CI identity — whatever credential the CI
runner holds. The fail-closed `/api/gate/check` pre-check is the sole
enforcement: if the server says the PR's gates are not satisfied, the apply
blocks before any stack is touched. The apply itself runs under the CI identity
— no elevation, no token minting.

With no server configured (`TFSTACKPLAN_SERVER` unset), the gate check is a
complete no-op: nothing gates and apply proceeds immediately.

### Privilege-backed deployment (`--impersonate-requester`)

```
tfstackplan run apply --dir stacks/prod --changed --impersonate-requester
```

`--impersonate-requester` turns the gate check into an identity source: it
takes the requester service-account email the server returns in the
`POST /api/gate/check` 200 response and mints a short-lived
`GOOGLE_OAUTH_ACCESS_TOKEN` for it via the IAM Credentials
`generateAccessToken` API. All subsequent `terraform` invocations in the same
process inherit that env var and run **as the leased PAM requester identity** —
the elevated identity that PAM actually granted the IAM-write role to.

**How it works end-to-end:**

1. The operator configures a PAM entitlement whose role binding grants the
   IAM-write permission to a small pool of requester service accounts
   (`approval "gcp-pam" { requester_pool = ["sa-applier-0@…", …] }`).
2. When `run plan` finalises a PR with an IAM gate, the server leases one pool
   SA per (PR, environment) — reused across all of that PR's gates in that
   environment — requests the PAM grant as that identity, and persists it on
   the `gate_targets` rows.
3. When the human approves the grant in GCP IAM, the server reconciles the gate
   to `ACTIVE`.
4. `run apply --impersonate-requester` calls `POST /api/gate/check`, receives
   `{"requester": "sa-applier-0@…"}`, calls `generateAccessToken` (the CI apply
   identity must hold `iam.serviceAccounts.getAccessToken` /
   `serviceAccountTokenCreator` on every pool SA), sets
   `GOOGLE_OAUTH_ACCESS_TOKEN`, and runs the Terramate apply script. Terraform
   picks up the token and applies as the elevated identity.
5. An unapproved IAM change fails at GCP (403) because the apply is not
   elevated — the enforcement is real, not only at the gate pre-check.

**When `--impersonate-requester` is a no-op:**

The flag is silently no-op when `GateCheck` returns an empty requester. Two
cases where this is expected and correct:

- **Gateless plan** — a clean plan with no IAM or classified changes has no
  gate targets, so no requester is leased; the gate check passes and returns
  `""`. The apply runs as the ambient CI identity, which is appropriate (no
  elevated permission is needed).
- **No server configured** — `TFSTACKPLAN_SERVER` is unset, `GateCheck` returns
  `("", nil)`, and the apply proceeds as normal.

A genuine misconfiguration where a classified IAM plan has an unsatisfied gate
still fails closed: either the gate pre-check returns a 409 (apply blocked) or
— if the gate check is somehow bypassed — GCP 403s the unelevated apply because
the CI identity lacks the IAM-write role.

**Additional CI requirements for `--impersonate-requester`:**

| Requirement | Detail |
|---|---|
| ADC with `serviceAccountTokenCreator` | The CI runner's ambient credential must hold `roles/iam.serviceAccountTokenCreator` (or `iam.serviceAccounts.getAccessToken`) on **every** SA in the requester pool. |
| Requester pool SAs hold the IAM-write role | The PAM entitlement grants the elevated role to the requester pool SAs, not to the CI runtime identity. |

The `GOOGLE_OAUTH_ACCESS_TOKEN` is process-scoped (set in the `run apply`
process's env and inherited by the subprocess); it is not written to disk or
exported beyond the apply run.

## Example: GitHub Actions

Two jobs — plan on PRs, apply on merge to the default branch. Each is a
checkout plus one `run` invocation.

```yaml
name: terraform
on:
  pull_request:
  push:
    branches: [main]

env:
  TFSTACKPLAN_SERVER: ${{ vars.TFSTACKPLAN_SERVER }}
  TFSTACKPLAN_TOKEN: ${{ secrets.TFSTACKPLAN_TOKEN }}
  TFSTACKPLAN_ENVIRONMENT: staging
  TFSTACKPLAN_REPO: ${{ github.repository }}

jobs:
  plan:
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    env:
      TFSTACKPLAN_PR: ${{ github.event.number }}
      TFSTACKPLAN_SHA: ${{ github.event.pull_request.head.sha }}
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 } # terramate --changed needs history
      # (install terramate, terraform, and the tfstackplan binary on PATH)
      - run: tfstackplan run plan --dir stacks/staging --changed --parallel 8

  apply:
    if: github.event_name == 'push'
    runs-on: ubuntu-latest
    env:
      TFSTACKPLAN_SHA: ${{ github.sha }}
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      # (install terramate, terraform, and the tfstackplan binary on PATH)
      # The PR number is recovered from the merge commit by the consumer and
      # exported as TFSTACKPLAN_PR so the apply gate check correlates.
      # --impersonate-requester is optional; omit it if you want advisory-only
      # enforcement (fail-closed gate pre-check, ambient CI identity for the apply).
      - run: tfstackplan run apply --dir stacks/staging --changed --base "${{ github.sha }}^" --parallel 8 --impersonate-requester
```

`run plan`/`run apply` shell out to `terramate` (and to `terraform` via the
scripts), so both must be on `PATH` (pin them in the repo's `.tool-versions`).
The plan job's report is also printed to stdout, so it is useful even without a
server. Branch protection requires the server's one check run per environment
(`plan/<environment>`); the apply gate pre-check blocks merge-time applies
until that environment's gates are approved.

## Local / offline

A human runs the same scripts directly, with no server:

```sh
terramate script run plan      # or: tfstackplan run plan --dir stacks/staging --changed
```

With `TFSTACKPLAN_SERVER` unset, every server interaction is a no-op:
`run tick` does nothing, `run plan` renders and prints the report, and
`run apply`'s gate check passes (nothing gates). This keeps the local loop
fast and dependency-free.

---

That's the end of the guide. From here it's all reference.

Next: [Reference →](../reference/index.md) — exhaustive flag tables, the full
`.tfstackplan.hcl` schema, and every environment variable. Or head back to
[the index](../index.md) for the whole table of contents.
