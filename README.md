# tfstackplan

One tool with **four faces** for multi-stack Terraform CI — a monorepo that
produces N plans per PR (Terramate / Terragrunt / multi-root-module):

- **`render`** — merge many `plan.json` files (one per stack) into a single,
  reviewer-friendly **markdown PR comment**, with optional classification. A
  pure, offline renderer — it never runs `terraform` and never posts.
- **`run`** — the **CI driver** that wraps your `terramate script run`, detects
  the changed stacks, runs plan/apply/verify, renders + classifies in-process,
  and reports the execution lifecycle to the control plane.
- **`serve`** — the **control plane**: a live dependency-DAG UI, approval gates,
  one GitHub check run per environment, and SSE-tailed per-stack logs.
- **`state`** — declarative **cross-stack Terraform state moves**, applied as
  part of the normal apply.

Module path: `github.com/Fluent-Health/terraform-stack-plan`.

`render` is fully standalone — you can use it on its own without ever touching
`run` / `serve` / `state`. The other faces layer a control plane on top while
Terraform keeps executing in *your* CI under *your* identities.

[tfplan2md]: https://github.com/oocx/tfplan2md

---

## What it looks like

A run over eight stacks with classification enabled, rendered the way GitHub
shows it in a PR comment: a scannable summary table, then a per-stack
drill-down. Inside a stack, **each resource is its own row within an indented
blockquote bar** — so it's always clear which stack/resource you're reading.
Small changes are shown expanded; big ones collapse to a row you click to open.
(The stacks below are shown expanded for illustration.)

<!-- tfstackplan:nonprod -->

### Terraform plan — nonprod  (8 stacks changed)

| Stack | Add | Change | Destroy | Replace | Categories |
| --- | ---: | ---: | ---: | ---: | --- |
| platform/nonprod | 0 | 4 | 0 | 0 | 🔐 iam |
| service-projects/app-dev | 4 | 3 | 0 | 0 | ✅ safe |
| data/warehouse | 0 | 0 | 6 | 0 | 💣 destructive |
| networking/shared-vpc | 0 | 5 | 0 | 2 | 💣 destructive |
| observability/grafana | 5 | 6 | 0 | 0 | ✅ safe |

<details open><summary>📁&nbsp;<b>platform/nonprod</b> · 🔐 iam · 4 change</summary>

>
> <details open><summary>✏️&nbsp;google_project_iam_member.data_engineers<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
>
> <details open><summary>✏️&nbsp;google_storage_bucket.tfstate<br>&nbsp;&nbsp;&nbsp;&nbsp;2 changed</summary>
>
> ```diff
> ~ retention_days = 7 → 30
> ~ labels (yaml):
>  env: nonprod
> +team: platform
> ```
>
> </details>

</details>

Scalars render as aligned `~ path = old → new` leaves; structured attributes
(here `labels`) render as a contextual diff. Creates and deletes look the same —
one open row per resource showing its attributes:

<details open><summary>📁&nbsp;<b>data/warehouse</b> · 💣 destructive · 6 destroy</summary>

>
> <details open><summary>➖&nbsp;google_bigquery_dataset.legacy_events<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_events"
> ```
>
> </details>

</details>

The first line of the real output is an HTML-comment marker
(`<!-- tfstackplan:nonprod -->`, invisible above) that CI uses to upsert one
comment per tier.

Key render behaviours:
- **Stacks read as section headers** (📁 + bold name); **every resource is a uniform `<details>` row** inside the stack's blockquote bar, giving a clear stack → resource hierarchy.
- **Row layout:** an emoji action glyph + the address on line 1 (glued with `&nbsp;` so a long path can't orphan the icon), with the descriptor (`N changed`, `replace`, …) hanging on an indented line below.
- **Size-based folding:** a row is open when its body is small (≤ ~10 lines), collapsed when big — the same rule for creates, deletes, and updates.
- **Aligned changes:** `~ path = old → new`, with `=` aligned and nested maps keeping their name via dotted paths (`+ labels.team = "platform"`). Diff-body markers stay ASCII `+/-/~` so GitHub colours them.
- **Structured values** (JSON/YAML strings and native HCL maps/lists) render as a **contextual diff** — the value canonically re-formatted, 2 lines of context, changed lines as `-`/`+`, tagged with its kind (`~ policy (json):`). Small diffs stay inline; big ones collapse the row.
- **State operations** surface as rows too: moved (↪️ `addr` / `moved from …`), imported (📥 `addr` / `imported · id=…`, the id monospaced), and removed-from-state (⏏️ `addr` / `forgotten`). These have no summary-table columns; their counts append to the stack's row text.

### More examples

Larger reports stay under GitHub's 65,536-byte comment cap by degrading the
biggest diffs first, then dropping detail. These files are real tool output
(regenerated and byte-checked by `go test ./cmd/tfstackplan`):

- [`examples/big-plan.md`](examples/big-plan.md) — 58 changes across 8 stacks,
  full detail, fits the default 60 KB budget.
- [`examples/over-budget-degraded.md`](examples/over-budget-degraded.md) —
  tighter budget: large diffs collapse to one-line summaries, small diffs kept.
- [`examples/over-budget-summary-only.md`](examples/over-budget-summary-only.md) —
  tighter still: all `<details>` dropped, summary table + a notice retained.
- [`examples/over-budget-minimal.md`](examples/over-budget-minimal.md) — past
  every simplification and still over budget: a one-line aggregate is emitted
  and the tool exits non-zero so CI can surface it.
- [`examples/state-ops.md`](examples/state-ops.md) — **moved** (↪️),
  **imported** (📥), and **removed-from-state / forget** (⏏️) resources, plus
  **contextual diffs** for nested JSON, YAML, and native HCL blocks.
- [`examples/long-names.md`](examples/long-names.md) — deeply nested module
  paths and **for-each `["key"]` indices**, rendered expanded to judge wrapping.

---

## Install / build

```bash
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest
# or
go build -o tfstackplan ./cmd/tfstackplan
```

A prebuilt binary is on the
[Releases](https://github.com/Fluent-Health/terraform-stack-plan/releases) page.
The release also ships a **multi-arch, distroless Cloud Run container** (its
entrypoint is `serve`) pushed to GHCR — the binary is fully static (pure-Go
SQLite, no cgo) and embeds its assets, so the image needs no runtime files.

### asdf

This repo is **its own [asdf](https://asdf-vm.com) plugin** — the hook scripts
live in [`bin/`](./bin), so there is no separate plugin repo to maintain. The
plugin downloads the prebuilt release binary (verifying its `.sha256`); no Go
toolchain is needed on the target machine.

```bash
asdf plugin add tfstackplan https://github.com/Fluent-Health/terraform-stack-plan.git
asdf install tfstackplan latest      # or a pinned version, e.g. 0.8.1
asdf set tfstackplan 0.8.1           # writes .tool-versions
```

linux/darwin on amd64/arm64. asdf won't auto-add the plugin from a
`.tool-versions`, so each machine runs `asdf plugin add …` once;
`asdf install` thereafter honours the pinned version.

---

## `render` — plan.json files → one markdown comment

Each stack contributes one `tfplan.json` (`terraform show -json plan.bin`).
Collect them under one directory that mirrors your stack tree —
`out/<stack>/tfplan.json` — and point the tool at it:

```bash
tfstackplan render --plans-dir out/ \
  --title  "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

A bare flags-first invocation (`tfstackplan --plans-dir …`) also renders, for
backward compatibility. Each `tfplan.json` found defines a stack; its **name**
is the directory holding it, relative to `--plans-dir` (so
`out/platform/nonprod/tfplan.json` → `platform/nonprod`). Stacks render
alphabetically. An empty (or absent) set of plans renders a header-only
"0 stacks changed" report.

### Flags

```
tfstackplan render --plans-dir DIR
            [--title TEXT] [--marker TEXT]
            [--config FILE]                 # HCL policy; default: auto-discover .tfstackplan.hcl (walks up to the repo root)
            [--max-bytes N]                 # default 60000; 0 disables the budget
            [--details auto|open|closed]    # default closed (auto = open iff one stack changed)
            [--emit-classification-json FILE]
            [--state-moves FILE]            # JSON manifest of cross-state move targets (see `state`)
            [--repo-root DIR]               # base for link file paths (default ".")
            [--link-var key=value]          # link template var (repeatable)
            [--output FILE | -]             # default '-' (stdout)
            [--version]
```

`--plans-dir` is required. Config auto-discovery walks **up** from the working
directory to the repo root (the first ancestor containing `.git`), so a command
run from a stack subdir — e.g. `run plan`/`run apply --dir stacks/<tier>` — finds
a repo-root `.tfstackplan.hcl` with no explicit `--config`. With no `--config` and
no `.tfstackplan.hcl` found, classification is off, diffs use defaults, and no
links are emitted — the tool degrades gracefully with zero config.

### Classification (presets + rules)

Classification is **computed by the tool**: each stack's plan is matched against
rules in the HCL policy. Every rule whose matcher fires contributes a category
(a stack carries the *set* it matched, in declaration order); a stack matching
nothing shows the `default`.

```hcl
classification {
  default { name = "safe", icon = "✅" }   # or shorthand: default = "safe"

  preset "iam" {            # built-in matcher (you only pick the icon)
    icon            = "🔐"
    emit_attributes = ["project"]   # surface matched subjects for CI gating
  }

  rule "destructive" {      # custom matcher; name = block label
    icon                  = "💣"
    resource_type_pattern = ".*"        # default: any type
    actions               = ["delete"]  # matches iff ALL listed actions appear
    min_count             = 1
  }
}
```

The **`iam` preset** matches IAM resources on GCP
(`*_iam_{policy,binding,member,audit_config}`), AWS (`aws_iam_*`), and Azure
(`azurerm_role_{assignment,definition}`); any action, so an in-place policy
update still flags. Classification considers only changes that **mutate the real
resource** (add/change/destroy/replace) — pure `move`/`import`/`forget` state
operations never contribute a category.

`--emit-classification-json` hands CI the result as data: per-stack `categories`
under `stacks`, plus a run-level `summary` with the per-key sorted-unique union
of emitted attributes — what a gate consumes. See
[`examples/.tfstackplan.hcl`](examples/.tfstackplan.hcl) for the canonical client
policy (classification, gating `class`, diff, links, progress).

### The byte budget

GitHub's 65,536-byte comment cap counts the raw markdown source (collapsed
`<details>` still counts). `fit` keeps the report under `--max-bytes` (default
60,000) by degrading the **largest diff first** — preferred → summary → hidden —
deterministically (byte-identical re-runs, so CI upserts don't churn). If even
all-minimal overflows, it cascades: summary-only → one-line aggregate → a
best-effort floor that exits non-zero. The summary table and classification are
**never** reduced.

### Diff config + links (optional)

A `diff {}` block tunes per-attribute diffs (force a `differ` when type
detection misfires); a `links {}` block adds header/stack/resource links
(resource → its `.tf` declaration at the commit, resolved by parsing the source
tree). Both live in the same `.tfstackplan.hcl`; see
[`examples/.tfstackplan.hcl`](examples/.tfstackplan.hcl).

---

## `run` — the CI driver

`run` wraps your `terramate script run` and reports the execution to the control
plane. It reads its context from the **environment** the orchestrator sets
(`internal/runner/env.go`); an empty server URL is a full no-op, so local runs
and `run tick` work offline:

| Env var | Meaning |
| --- | --- |
| `TFSTACKPLAN_SERVER` | control-plane base URL (`""` = offline, no-op) |
| `TFSTACKPLAN_TOKEN` | bearer secret for `/api/*` |
| `TFSTACKPLAN_EXECUTION` | execution id this run reports under |
| `TFSTACKPLAN_STACK` | current stack path (fallback for `run tick --stack`) |
| `TFSTACKPLAN_ENVIRONMENT` | deployment environment for the execution |
| `TFSTACKPLAN_PR` | PR number |
| `TFSTACKPLAN_REPO` | `owner/repo` |
| `TFSTACKPLAN_SHA` | head commit SHA |

Server reporting is **best-effort**: a down or absent server degrades the build
to "no live progress", never to failure. The apply-time gate check is the one
**fail-closed** exception.

### `run plan`

```
tfstackplan run plan --dir DIR
        [--changed]          # only plan changed stacks (default true)
        [--parallel N]       # parallel plan jobs (0 = terramate default)
        [--base REF]         # git base ref for change detection
        [--script NAME]      # terramate script name (default "plan")
        [--log-file NAME]    # per-stack log file the script tees (default tfstackplan.log; empty disables)
        [--config FILE]      # default: auto-discover .tfstackplan.hcl under --dir
```

Detects the changed stacks, registers the execution + dependency DAG (`Init`),
runs the terramate plan script across the changed set (setting the
`TFSTACKPLAN_*` env so each stack's `run tick` reports progress), gathers each
stack's `tfplan.json`, renders + classifies **in-process**, derives the approval
gates (each gating `class` × its emitted target values) and the moving stacks,
and posts `Finalize`. Per-stack logs stream live to the server from the
`--log-file` each stack's terramate script tees terraform output to.

### `run apply`

```
tfstackplan run apply --dir DIR
        [--changed] [--base REF] [--script NAME]   # default script "apply"
        [--log-file NAME]
        [--state-lock]       # pessimistic GCS lock around cross-state moves (requires ADC)
```

1. **Fail-closed gate pre-check** — asks the server whether the PR's gates are
   satisfied *before touching terramate*; a 409, any non-2xx, or an unreachable
   *configured* server blocks the apply (an unconfigured server is a no-op pass).
2. **Cross-state move pre-phase** — executes any pending `_tfsp_xmove.*.hcl`
   manifests (see [`state`](#state--cross-stack-state-moves)); `--state-lock`
   wraps them in the pessimistic GCS lock. Fail-closed.
3. Applies the changed stacks **in dependency order** (no `--parallel`), then
   revokes the PR's grants afterward (best-effort).

### `run verify`

```
tfstackplan run verify --dir DIR [--changed] [--base REF] [--script NAME] [--log-file NAME]
```

Runs the terramate `verify` script (default) across changed stacks — **no gate**,
read-only post-apply validation — and reports a `verify/<env>` check run with its
own live page and per-stack Verify tab.

### `run tick`

```
tfstackplan run tick [--stack PATH] [--status STATUS] [--detail TEXT]
```

The internal per-stack reporter the terramate scripts call between commands. It
reads the execution context from the `TFSTACKPLAN_*` env, posts a best-effort
`update`, and is a no-op offline or on any server error — a tick never fails the
build.

---

## `serve` — the control plane

```
tfstackplan serve [--config FILE] [--addr :8080]
```

`serve` ties the server together from the `serve {}` config block (see
[Configuration](#configuration-tfstackplanhcl)): opens the SQLite store, builds
the real GitHub App client and the gcp-pam approval backend (from ADC), starts
the reconcile loop, and serves. Public read routes (same sensitivity as plan
output already on the PR, behind unguessable execution ids); `/api/*` is
bearer-authed.

- **Live DAG.** The execution renders as a **group-level** dependency graph:
  stacks fold into group nodes by their path → `env/kind` (configurable via the
  `group {}` block — depth or regexp), laid out in **per-environment swimlanes**,
  each node showing its stack-count, **worst status**, and 🔐/💣 category badges.
  An inert, self-contained SVG (survives GitHub's image proxy).
- **Drill-down.** A folding per-stack list (grouped by the same key); each stack
  links to a detail page with **Log / Plan / Verify** tabs, **live-tailed via
  Server-Sent Events** (no polling refresh).
- **Navigation.** An execution index at `/` (most recent first) and a per-PR
  timeline at `/pr/{n}`.
- **Approval gates** keyed `(class, target)` — multiple approval classes, each
  binding a classification class to a PAM entitlement and scope. The server only
  ever *requests* a grant; humans approve in the backing provider. The **GCP PAM**
  backend uses true requester leasing (impersonates the first pool identity with
  no open grant, falling back to a `PR mod pool` slot only when exhausted).
- **GitHub checks.** One check run per environment (`plan/<env>` — the same name
  as the commit-status context, so branch protection requires one consistent
  context). Rich check runs are always used.
- **Pub/Sub push ingestion** (OIDC-verified) as a latency win over the poll loop.

See [`examples/serve.tfstackplan.hcl`](examples/serve.tfstackplan.hcl) for the
full config and [Deployment](#deployment) below.

---

## `state` — cross-stack state moves

`tfstackplan state` is operator-driven, declarative Terraform state-move
machinery. `state move` writes **PR-keyed shim files** that the normal `run
apply` then applies — no out-of-band `terraform state` surgery in the apply path.

```
tfstackplan state move --dir DIR [--stack STACK] [--pr N] [--via mv] <from> <to> …
tfstackplan state list    --dir DIR [--pr N]
tfstackplan state cleanup --dir DIR (--pr N | --all)
tfstackplan state apply   --dir DIR [--execute] [--lock]
```

`state move` routes each `<from> <to>` pair by comparing the two sides' stacks
(`--stack` is the default for unqualified addresses; an explicit `stack:addr`
prefix overrides). All pairs are validated against the relevant `tfplan.json`(s)
before anything is written (fail-closed):

- **Same-stack** → a native `moved {}` block.
- **Cross-stack** → an `import { to id }` block in the destination shim (the `id`
  is read from the destroyed resource's `before.id` in the source plan) + a
  `removed { … lifecycle { destroy = false } }` block in the source shim — the
  resource is adopted into the new state and dropped from the old without being
  destroyed.
- **`--via mv`** → instead records a `_tfsp_xmove.<key>.hcl` manifest in the
  destination stack, applied by the faithful `terraform state mv` executor
  (`state apply`) rather than by `run apply`.

Shims are keyed `PR-<n>` (from `--pr` / `$TFSTACKPLAN_PR`), else `branch-<name>`,
else `local`. `state apply` discovers every `_tfsp_xmove.*.hcl` manifest and runs
it via terraform-exec (pull → back up under `.tfsp-state-backups` → per-pair
fail-closed decision → `state mv` → push, never `--force`). It is **dry-run by
default**; `--execute` performs the moves, and `--lock` adds a pessimistic GCS
lock. The same executor runs in the `run apply` cross-state-move pre-phase
(always `--execute`, behind `--state-lock`).

On the projecting side, `render --state-moves moves.json` classifies cross-state
move-targets (their planned *creates*) as relocations, so they don't trip the
per-project IAM gate.

---

## Configuration (`.tfstackplan.hcl`)

One HCL file drives all four faces; every block is optional and
backward-compatible (a render-only file needs none of the server blocks).

| Block | Used by | Purpose |
| --- | --- | --- |
| `classification {}` | render, run | presets / rules / `default`, with `emit_attributes` + `derive {}` |
| `diff {}` | render | per-attribute diff defaults + overrides (`detect`, `max_attribute_lines`, `rule {}`) |
| `links {}` | render | header / stack / resource URL templates |
| `server {}` | run, serve | `url`, `environment` |
| `class "<name>" {}` | run, serve | `backend`, `entitlement`, `entitlement_scope`, `required` — bind a class to an approval gate |
| `serve {}` | serve | the control-plane runtime (below) |

The `serve {}` block (real field names):

```hcl
serve {
  db_path            = "/data/tfstackplan.db"
  public_base_url    = "https://tfstackplan.example.com"
  webhook_secret_env = "TFSTACKPLAN_WEBHOOK_SECRET"  # env var NAME, not the secret
  logs_dir           = "/data/logs"

  github_app {
    app_id           = "123456"
    installation_id  = "78901234"
    private_key_path = "/secrets/github-app.pem"
  }

  approval "gcp-pam" {                  # block label = backend
    location       = "global"
    duration       = "28800s"
    requester_pool = ["sa0@…", "sa1@…"]
  }

  group   { depth = 2 }                 # or: pattern = "regexp" (first capture = group key)
  objects { backend = "gcs", bucket = "tfstackplan-logs", prefix = "executions" }
  pubsub  { audience = "…/pubsub/push", service_account = "…@….gserviceaccount.com" }
}
```

The full, commented reference is
[`examples/serve.tfstackplan.hcl`](examples/serve.tfstackplan.hcl) (kept valid by
a parse test).

---

## Deployment

`serve` runs as a Cloud Run-class service:

- **Container** — point Cloud Run at the released distroless image
  (`ghcr.io/<org>/tfstackplan:<tag>`, entrypoint `serve`). Single instance per
  environment (the SQLite store is single-writer by design; WAL + busy_timeout
  set at the DSN level).
- **Logs** — set `serve { logs_dir }` for per-stack buffers and `objects { … }`
  for GCS offload of completed-stack logs (served back via a stored pointer, so
  viewers need no cloud IAM).
- **Pub/Sub** — `serve { pubsub { … } }` enables OIDC-verified push ingestion as
  a latency win over the poll loop.
- **Credentials** — Application Default Credentials supply the GCP creds for PAM
  (impersonation), GCS, and OIDC verification; the GitHub App key is a mounted
  PEM file (`github_app { private_key_path }`).

Deeper notes live in `docs/deploy-cloud-run.md`, `docs/ci-integration.md`, and
`SECURITY.md`.

---

## Out of scope

- **Posting** to GitHub / GitLab / Bitbucket — `render` writes markdown; your CI
  posts it. (`serve` does post checks/statuses for its own check runs.)
- **Running `terraform plan`** in `render` — its inputs are pre-existing
  `plan.json` files. (`run` drives terramate, which runs terraform.)
- **Static-analysis rollup** (Checkov, Trivy, SARIF) — possible later.

See [`docs/DESIGN.md`](docs/DESIGN.md) for architecture and design rationale.

## Related tools

- [`tfplan2md`][tfplan2md] — single-plan markdown renderer; inspiration for the
  per-stack diff style.
- [Atlantis](https://www.runatlantis.io/) — PR-comment-driven Terraform CI;
  renders per-project, tied to its own workflow.
- [`terraform-plan-summary`](https://github.com/dineshba/terraform-plan-summary) —
  single-plan summary table; no multi-plan support.

---

## Contributing

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). For security
issues, follow [SECURITY.md](SECURITY.md) rather than opening a public issue.

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE).

---

Built and maintained by [Fluent Health](https://github.com/Fluent-Health).
