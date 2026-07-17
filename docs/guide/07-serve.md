# 07 — `serve`

> Governance is not a checkbox. It's the thing between "merged" and "regret."

The governance gap is real: once `render` gives reviewers a readable plan, there
is still nothing between a human approving a PR and `terraform apply` running
unchecked across every stack that changed. `serve` is that thing.

![A dog in a hard hat sits calmly at a terminal in a server room as one line reads "Destroying... google_sql_database_instance.prod"](../images/aside-this-is-fine.jpg)

It is a long-running control-plane service — a single binary you run on Cloud
Run — that gives a multi-stack execution a **live dependency-DAG UI**, **approval
gates**, **one GitHub check run per environment**, and **SSE-tailed per-stack
logs**. A PR with a destructive or IAM change cannot apply until a human has
approved it. A reviewer can watch the apply happen in real time.

## What `serve` is — and what it is not

Here is the boundary that matters: `serve` **observes and gates**. It does not
hold your cloud credentials. It does not run `terraform` for you.

The actual plan and apply keep running in your own CI, under your own service
accounts, using the identities your pipeline already carries. `run plan` and
`run apply` call your `terramate script run` exactly as they would at a laptop.
`serve` hears about each lifecycle event through the `TFSTACKPLAN_*` protocol
and reacts — surfacing a check run, updating a DAG node, holding a gate — but
it never touches a Terraform state file.

This matters because it means you can add governance without handing a service
the keys to your infrastructure. The separation is structural, not a policy
choice.

## The live UI — the central `tfstackplan ui` service

When `run plan` registers an execution, `serve` creates a per-environment GitHub
check run (`plan/<env>` — or the consolidated `terraform/<env>`, see
[09 — CI integration](09-ci-integration.md), on a tier where serve drives CI
runs itself) before a single plan has finished. The check run's Details link
lands on the **central UI** (`tfstackplan ui`) — a separate service, one pane of
glass over every tier serve, behind a Google Workspace login:

- **Home** — recent executions per tier, with pending approvals surfaced on top.
- **PR view** (`/pr/{n}`) — one PR's full plan/apply/verify story across tiers.
- **Execution view** — live progress, approval gates (approve/deny in-UI under
  your own Google identity), and a per-stack list with **Plan** and **Log**
  tabs, live-tailed via Server-Sent Events with in-place updates — no polling,
  no page reloads.
- **Approvals** — every gate awaiting a human, across tiers.

The tier serves themselves no longer serve HTML pages; they expose the data —
a JSON/SSE API plus an OIDC-scoped `GET /plan/<id>/<stack>` HTML fragment — that
the central UI aggregates and renders. The dependency DAG is drawn client-side
by the UI, not baked into the check run.

## Approval gates

Classification (from `render` / `run plan`) can mark a stack's changes as IAM
or destructive. A `class "<name>" {}` block in `.tfstackplan.hcl` binds a
classification class to an approval gate backed by GCP PAM:

```hcl
class "iam" {
  backend           = "gcp-pam"
  entitlement       = "projects/my-project/…/entitlements/terraform-iam"
  entitlement_scope = ["project:my-project"]
  required          = true
}
```

When `run plan` finalizes, `serve` derives the gate targets — each classification
class paired with its emitted `project` (or other `emit_attributes`) values — and
requests a PAM grant for each one. A human approves in GCP IAM. The server
reconciles the gate state as approvals come in.

The check run (`plan/<env>`, or `terraform/<env>` when serve drives CI runs
itself — see the `executor` block in [09 — CI integration](09-ci-integration.md))
reflects gate status in its summary. Branch protection requires this one check
run per environment, so an unapproved gate blocks the merge — and `run
apply`'s fail-closed gate pre-check blocks the apply even if a merge somehow
slips through. There is no code path where an unsatisfied gate lets an apply
proceed.

## Privilege-backed apply

By default, `serve` operates in **advisory mode**: the gate pre-check is the
enforcement — it blocks `run apply` if the gates are not satisfied — but the
apply itself runs under the ambient CI identity, the same one the runner has
always used. No token minting, no identity switching. This is appropriate when
your CI runner already holds the right IAM role for a given environment.

`--impersonate-requester` turns that around: when the gate is satisfied, the
server returns the leased requester service-account email alongside the approval.
`run apply` mints a short-lived access token for that SA and sets it as
`GOOGLE_OAUTH_ACCESS_TOKEN`, so every subsequent terraform invocation runs as the
**PAM-elevated identity** — the one that actually holds the IAM-write role the
entitlement grants. The CI runner's ambient identity does not need that role.

The practical consequence: an unapproved IAM change fails at GCP (403) because
the apply is not elevated. The enforcement is real, not only at the gate pre-check.

When the plan has no classified changes — nothing to gate — the server returns
an empty requester, and `--impersonate-requester` is a silent no-op. The apply
runs under the ambient identity, which is appropriate: no elevated permission
is needed.

The exact IAM wiring — requester pool SAs, `serviceAccountTokenCreator` grant on
the CI runner, ADC requirements — is in [09 — CI integration](09-ci-integration.md).

## Event-sourced internals

One paragraph, because the architecture answers a question you might have: how
does the server recover state after a restart, or correctly audit what happened
to a gate?

The event log is the source of truth. Every gate lifecycle transition —
`GateSatisfied`, `TargetRevoked`, `ExecutionFailed`, and their siblings — is
appended as an immutable past-tense fact. The live gate state (`NotClassified`,
`Pending`, `Satisfied`, `Blocked`, …) is a projection folded from that log via
the `Decide`/`Evolve`/`React` decider. A restart replays the tail of the log
over the last snapshot and converges to the correct state — no reconciliation
job, no separate source to go out of sync. The full architecture, stream scopes,
and projection design are in [`../architecture/`](../architecture/) and
[`../DESIGN.md`](../DESIGN.md).

## Deployment sketch

`serve` is a Cloud Run-class service. The released distroless image
(`ghcr.io/<org>/tfstackplan:<tag>`) has `serve` as its entrypoint. It runs as a
single instance — the SQLite store is single-writer by design — and needs no
runtime files beyond a mounted PEM (GitHub App private key) and Application
Default Credentials for GCP.

The `serve {}` config block in `.tfstackplan.hcl` controls everything: the DB
path, the GitHub App, the PAM backend, the log storage, and the optional Pub/Sub
push ingestion path. The commented reference config is at
[`examples/serve.tfstackplan.hcl`](../../examples/serve.tfstackplan.hcl).

Deeper deployment notes — Cloud Run service config, IAM grants, secrets — are in
[`../reference/install-and-deploy.md`](../reference/install-and-deploy.md) and
[`09-ci-integration.md`](09-ci-integration.md).

---

Next: [08 — `state` →](08-state.md)
