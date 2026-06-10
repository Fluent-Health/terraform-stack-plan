# CI integration: driving plan/apply with `tfstackplan run`

`tfstackplan run` is the CI driver. It wraps the *same* `terramate script run`
a human invokes, so a PR pipeline shrinks to **checkout + one `run` invocation**
per phase — change detection, parallel plan, per-stack progress, in-process
render + classification, and the approval gate are all handled by the binary,
not by bash glue.

This is additive: `terramate script run plan` still works for a human at a
laptop, and `tfstackplan run` with no server configured is fully offline (it
renders the report locally and never blocks).

## The pieces

```
PR opened ──▶ CI plan job ──▶ tfstackplan run plan  ──┐
                                                       ├─▶ tfstackplan serve  ──▶ GitHub check run + live UI
merge     ──▶ CI apply job ─▶ tfstackplan run apply ──┘        (control plane)        + approval gates
                  └─ each invokes `terramate script run …` (the same scripts a human runs)
```

- **`run plan`** — detects the changed stacks (`terramate list --changed`),
  registers the execution + DAG with the server, runs the terramate `plan`
  script across the changed set (parallel), gathers each stack's `tfplan.json`,
  renders + classifies in-process, and finalizes the report + approval gates.
- **`run apply`** — runs a **fail-closed gate pre-check** (refuses to apply
  unless the server says the PR's gates are approved), then applies the changed
  stacks in dependency order, then revokes the grants.
- **`run tick`** — the per-stack progress reporter the terramate scripts call;
  a no-op offline, so the scripts stay portable.

## Environment

The `run` commands read their context from the environment (so nothing is
threaded through the terramate scripts as flags):

| Variable | Meaning |
|----------|---------|
| `TFSTACKPLAN_SERVER` | control-plane base URL. **Empty ⇒ fully offline** (no posts; `run tick` no-ops; the apply gate check passes — nothing gates). |
| `TFSTACKPLAN_TOKEN` | bearer secret for the server's `/api/*`. |
| `TFSTACKPLAN_ENVIRONMENT` | the deployment environment this run targets (e.g. `staging`, `prod`). |
| `TFSTACKPLAN_EXECUTION` | execution id. Set it to correlate plan+apply of the same PR; if unset, `run plan`/`run apply` generate one. The orchestrator exports it so the script's `run tick` reports under the same id. |
| `TFSTACKPLAN_REPO` / `TFSTACKPLAN_SHA` / `TFSTACKPLAN_PR` | repo `owner/name`, commit SHA, and PR number — used to build the check run / commit status and to correlate approval grants. |

A down or absent server degrades the build to "no live progress" — never to
failure — **except** the apply gate pre-check, which is deliberately fail-closed.

## Terramate scripts

The consumer defines `plan` and `apply` scripts once (root `scripts.tm.hcl`,
inherited by every stack). They run terraform and call `run tick` — the runner
invokes these exact scripts, so there is one execution code path. Enable scripts
in the root config and pass the per-stack path to `run tick` via terramate's
stack metadata:

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
      ["tfstackplan", "run", "tick", "--stack", "${terramate.stack.path.relative}", "--status", "running"],
      ["terraform", "init", "-input=false", "-lock=false"],
      ["terraform", "plan", "-input=false", "-lock=false", "-out=plan.bin"],
      # Emit the plan JSON where `run plan` gathers it: <stack>/tfplan.json.
      ["sh", "-c", "terraform show -json plan.bin > tfplan.json"],
      ["tfstackplan", "run", "tick", "--stack", "${terramate.stack.path.relative}", "--status", "planned"],
    ]
  }
}

script "apply" {
  description = "init + apply, report progress"
  job {
    commands = [
      ["tfstackplan", "run", "tick", "--stack", "${terramate.stack.path.relative}", "--status", "running"],
      ["terraform", "init", "-input=false"],
      ["terraform", "apply", "-input=false", "-auto-approve", "plan.bin"],
      ["tfstackplan", "run", "tick", "--stack", "${terramate.stack.path.relative}", "--status", "safe"],
    ]
  }
}
```

Notes:
- `run plan` gathers each stack's `tfplan.json` from the stack directory
  (`<stack>/tfplan.json`), so the `show -json` step writes there.
- `run tick` is a no-op when `TFSTACKPLAN_SERVER` is unset and never fails the
  build, so these scripts run unchanged at a laptop.
- A failed stack should call `tfstackplan run tick --status failed --detail "…"`
  (e.g. in an `after_failure`-style wrapper) so the live view shows it; on a
  hard terramate failure `run plan` still finalizes with `failed=true`.

## Example: GitHub Actions

Two jobs — plan on PRs, apply on merge to the default branch. Each is a checkout
plus one `run` invocation.

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
      - run: tfstackplan run apply --dir stacks/staging --changed --base "${{ github.sha }}^"
```

`run plan`/`run apply` shell out to `terramate` (and `terraform` via the
scripts), so both must be on `PATH` (pin them in the repo's `.tool-versions`).
The plan job's report is also printed to stdout, so it is useful even without a
server. Branch protection requires the server's one check run per environment
(`plan/<environment>`); the apply gate pre-check blocks merge-time applies until
that environment's gates are approved.

## Local / offline

A human runs the same scripts directly, with no server:

```sh
terramate script run plan      # or: tfstackplan run plan --dir stacks/staging --changed
```

With `TFSTACKPLAN_SERVER` unset, every server interaction is a no-op: `run tick`
does nothing, `run plan` renders + prints the report, and `run apply`'s gate
check passes (nothing gates). This keeps the local loop fast and dependency-free.
