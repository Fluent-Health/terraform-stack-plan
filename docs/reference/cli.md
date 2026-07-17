# CLI reference

`tfstackplan` — one binary, nine top-level commands.

```
tfstackplan <subcommand> [flags]
tfstackplan [flags]           # bare flags invoke render (backward-compat)
```

Subcommands: `render`, `run`, `serve`, `ui`, `state`, `claims`, `admin`, `catalog`, `whoami`.

See also: [`configuration.md`](configuration.md) for the `.tfstackplan.hcl`
schema, [`environment.md`](environment.md) for `TFSTACKPLAN_*` env vars.

---

## `render`

Reads one `tfplan.json` per stack from a directory tree, renders a
reviewer-friendly Markdown comment, and writes it to a file or stdout.

```
tfstackplan render --plans-dir DIR [flags]
tfstackplan        --plans-dir DIR [flags]   # backward-compat alias
```

`--plans-dir` is required. Each `<stack>/tfplan.json` found under the dir
defines one stack; the stack name is its relative path within that dir.

Config auto-discovery walks **up** from the working directory to the nearest
`.git` ancestor. With no config found, classification is off, diffs use
defaults, and no links are emitted.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--plans-dir` | string | _(required)_ | Directory of per-stack plans; each `<stack>/tfplan.json` defines one stack. |
| `--title` | string | `"Terraform plan"` | Report heading written into the Markdown output. |
| `--marker` | string | `"tfstackplan"` | HTML-comment marker used by CI to locate and upsert the comment. |
| `--config` | string | `""` | HCL policy file. Default: auto-discover `.tfstackplan.hcl` walking up from cwd. |
| `--max-bytes` | int | `60000` | Document byte budget. `0` disables the budget. The fit pass degrades the largest diff first; the summary table is never reduced. |
| `--details` | string | `"closed"` | `<details>` disclosure: `auto` (open iff exactly one stack changed), `open`, or `closed`. |
| `--emit-classification-json` | string | `""` | Write computed per-stack categories and run-level summary as JSON to this file. |
| `--state-moves` | string | `""` | Path to a JSON manifest of pending cross-state move targets (`{"<stack>":["<addr>",…]}`). Their planned creates classify as moves (non-IAM). Keys must match the `--plans-dir` stack names. |
| `--repo-root` | string | `"."` | Base directory for computing link file paths. |
| `--link-var` | string (repeatable) | — | Link template variable as `key=value`. `sha=<sha>` also derives `sha_short`. May be repeated. |
| `--output` | string | `"-"` | Output file. `"-"` writes to stdout. |
| `--version` | bool | `false` | Print version string and exit. |

**Exit codes:** `0` success; `1` error; `2` flag error or report exceeds
`--max-bytes` even after full reduction (warning printed to stderr, output
still written).

---

## `run`

Wraps `terramate script run` and reports execution progress to the control
plane. All `run` subcommands read their server context from the
`TFSTACKPLAN_*` environment (see [`environment.md`](environment.md)).
An empty `TFSTACKPLAN_SERVER` is a full no-op — local runs and offline
script invocations work without a server.

```
tfstackplan run <subcommand> [flags]
```

Subcommands: `lint`, `plan`, `apply`, `verify`, `tick`, `wrap`, `phase`, `register` (`step` also still works as a deprecated alias for `wrap`).

### `run lint`

Registers the execution on the server, transitions to the `PhaseLinting` phase,
and executes the Terramate lint script across stacks. If linting fails, it cleanly
finalizes the execution with `Failed=true` so CI pipelines and check runs don't hang,
and exits with code `1`. On success, it exits `0` without finalizing (leaving the global
execution active for subsequent plan/apply steps).

```
tfstackplan run lint --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--changed` | bool | `true` | Only lint changed stacks (terramate `--changed`). |
| `--parallel` | int | `0` | Parallel lint jobs. `0` uses the terramate default. |
| `--base` | string | `""` | Git base ref for change detection. |
| `--script` | string | `"lint"` | Terramate script name to run. |

### `run plan`

Detects changed stacks, registers the execution and dependency DAG, runs the
terramate plan script, renders and classifies the resulting plans, derives
approval gates, and posts a finalize event.

```
tfstackplan run plan --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--changed` | bool | `true` | Only plan changed stacks (terramate `--changed`). |
| `--parallel` | int | `0` | Parallel plan jobs. `0` uses the terramate default. |
| `--base` | string | `""` | Git base ref for change detection. |
| `--script` | string | `"plan"` | Terramate script name to run. |
| `--log-file` | string | `"tfstackplan.log"` | Per-stack log filename written by the terramate script in each stack dir; streamed live to the server. Empty string disables log streaming. |
| `--config` | string | `""` | HCL config for the classify pass. Default: auto-discover `.tfstackplan.hcl` under `--dir`. |

### `run apply`

Performs a fail-closed gate pre-check, executes any pending cross-state move
manifests, then runs the terramate apply script. Always emits a terminal
finalize event; revokes grants afterward (best-effort).

```
tfstackplan run apply --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--changed` | bool | `true` | Only apply changed stacks. |
| `--parallel` | int | `0` | Parallel apply jobs. `0` = terramate default (serial). Terramate still honors the dependency DAG regardless of this value. |
| `--base` | string | `""` | Git base ref for change detection. |
| `--script` | string | `"apply"` | Terramate script name to run. |
| `--log-file` | string | `"tfstackplan.log"` | Per-stack log filename; streamed live to the server. Empty string disables streaming. |
| `--state-lock` | bool | `false` | Acquire a pessimistic GCS state lock around cross-state moves. Fail-fast if the lock is already held. Requires ADC. |
| `--impersonate-requester` | bool | `false` | Run the apply as the leased PAM requester service account (mints `GOOGLE_OAUTH_ACCESS_TOKEN` for it). A classify pass failure under this flag is fail-closed. |
| `--config` | string | `""` | HCL config for the classify pass. Default: auto-discover `.tfstackplan.hcl` under `--dir`. |

The gate pre-check is fail-closed: a 409, any non-2xx, or an unreachable
_configured_ server blocks the apply. An unconfigured server (`TFSTACKPLAN_SERVER`
unset) is a no-op pass.

### `run verify`

Runs the terramate verify script across changed stacks and reports a
`verify/<env>` check run. No gate check — verification is read-only
post-apply validation.

```
tfstackplan run verify --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--changed` | bool | `true` | Only verify changed stacks. |
| `--base` | string | `""` | Git base ref for change detection. |
| `--script` | string | `"verify"` | Terramate script name to run. |
| `--log-file` | string | `"tfstackplan.log"` | Per-stack log filename; streamed live to the server. Empty string disables streaming. |

### `run tick`

Reports one stack's status to the server (best-effort). Called by the
terramate script between commands. Reads server context from `TFSTACKPLAN_*`
env. Always exits `0` — a tick must never fail the build.

```
tfstackplan run tick [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--stack` | string | `""` | Stack path. Defaults to `$TFSTACKPLAN_STACK`. |
| `--status` | string | `""` | Stack status to report (e.g. `running`, `planned`, `failed`). |
| `--detail` | string | `""` | Optional failure detail string. |

A tick is a no-op when `--status`, `--stack`, or `TFSTACKPLAN_EXECUTION` is
empty, or when no server is configured.

### `run wrap`

Wraps one stack command: reports a start status, streams output as log chunks,
then reports the terminal status based on the command's exit code and output.
Always exits with the wrapped command's exit code.

```
tfstackplan run wrap [flags] -- <command> [args...]
```

`--` is required; everything after it is the wrapped command.

`run step` is kept as a deprecated alias for `run wrap` — same flags, same
behaviour, plus a one-line deprecation warning on stderr — pending removal
once CI scripts switch over.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--stack` | string | `""` | Stack path. Defaults to `$TFSTACKPLAN_STACK`. |
| `--on-success` | string | `""` | Status to report on a zero exit (e.g. `safe`, `planned`). Empty means intermediate — nothing is reported on success. |
| `--running` | string | `""` | Status to report on start. Default: `running`. |
| `--tty` | bool | `false` | Run the command under a PTY so it emits ANSI colour. Falls back gracefully to a pipe if a PTY is unavailable. |

Status classification for `run wrap`:

- Non-zero exit → `failed`
- Exit 0 + apply summary with all-zero counts → `nochange`
- Exit 0 + apply summary with any non-zero count → `safe` (or `--on-success` if set)
- Exit 0 + no apply summary → `--on-success` (may be empty = no terminal tick)

### `run phase`

Reports a lifecycle phase to the server (best-effort). Reads `TFSTACKPLAN_EXECUTION`
from the environment. Always exits `0`. A no-op when no execution id or server
is configured.

```
tfstackplan run phase [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--phase` | string | `""` | Lifecycle phase. Valid values: `warming`, `linting`, `initializing`, `planning`, `applying`, `testing`, `verifying`. |

### `run register`

Lists the stack set and registers it with the server (one `Init` event, all
stacks pending) before the plan or apply phase. Enables the server to show the
real stack count during warming and initializing phases. No-op offline.
Never fails the build except on a flag-parse error.

```
tfstackplan run register [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | `"."` | Terramate root directory. |
| `--changed` | bool | `false` | Only register changed stacks (passes `--changed` to terramate). |
| `--base` | string | `""` | Git base ref for `--changed`. |

### `run exec`

Runs a single command with optional lifecycle-phase narration and fail-closed
check-run finalization. A transparent passthrough when no server is
configured (`TFSTACKPLAN_SERVER` unset). On a non-zero exit it finalizes the
execution as failed (best-effort) before propagating the command's exit code.

```
tfstackplan run exec [flags] -- <command> [args...]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--phase` | string | `""` | Lifecycle phase to tick before running (`warming`\|`linting`\|`initializing`\|`planning`\|`applying`\|`testing`\|`verifying`). |

### `run status`

Fetches and prints one execution's status from the server, optionally
blocking to watch for real-time updates via the server's SSE events stream.
Exits `1` if the execution's terminal status is `failure`.

```
tfstackplan run status [flags] <execution-id>
```

The execution ID may also be supplied via `$TFSTACKPLAN_EXECUTION`. The
server URL is resolved from `--server`, else `$TFSTACKPLAN_SERVER`, else
auto-discovered from the local `.tfstackplan.hcl`.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--server` | string | `""` | Server base URL. Defaults to `$TFSTACKPLAN_SERVER`, then auto-discovery from the local `.tfstackplan.hcl`. |
| `--format` | string | `"text"` | Output format: `text` or `json`. |
| `--watch` | bool | `false` | Block and watch for real-time status updates. |

### `run claims`

Alias under the `run` group for the top-level `claims` command — same
subcommands (`list`, `release`) and flags. See [`claims`](#claims).

```
tfstackplan run claims <subcommand> [flags]
```

### `run whoami`

Alias under the `run` group for the top-level `whoami` command — same
flags. See [`whoami`](#whoami).

```
tfstackplan run whoami [flags]
```

---

## `serve`

Starts the control-plane server: opens the SQLite store, builds GitHub App and
GCP PAM clients from ADC, starts the reconcile loop, and listens for HTTP
requests.

```
tfstackplan serve [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--config` | string | `".tfstackplan.hcl"` | HCL config file. Must contain a `serve {}` block. |
| `--addr` | string | `":8080"` | TCP listen address. |
| `--demo` | bool | `false` | Boot in credential-free demo mode with seeded data (ephemeral SQLite in a temp dir). |

All runtime configuration (GitHub App credentials, PAM entitlements, GCS log
bucket, token secret, etc.) lives in the `serve {}` block of the HCL config.
See [`configuration.md`](configuration.md) for the full schema and
[`install-and-deploy.md`](install-and-deploy.md) for deployment guidance.

---

## `ui`

The central aggregator face — a stateless single pane of glass over the tier
serves. Reads its own `ui {}` block from the HCL config, resolves per-tier
OIDC tokens, and (optionally) wires Google OAuth login and a GitHub webhook
relay. The `ui {}` block is not yet covered in [`configuration.md`](configuration.md);
see `internal/config`'s `UIConfig` in the meantime.

```
tfstackplan ui [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--config` | string | `".tfstackplan.hcl"` | HCL config file. Must contain a `ui {}` block. |
| `--addr` | string | `":8081"` | Listen address. |

---

## `state`

Operator-driven, declarative cross-stack state-move machinery. `state move`
writes PR-keyed shim files; `run apply` picks them up and executes them.

```
tfstackplan state <subcommand> [flags]
```

Subcommands: `move`, `import`, `remove`, `list`, `moves-manifest`, `apply`, `cleanup`, `check`.

### `state move`

Validates `<from> <to>` address pairs against the relevant `tfplan.json`(s)
and writes shim files. Same-stack pairs produce a `moved {}` block; cross-stack
pairs produce `import`/`removed` blocks (or an `_tfsp_xmove.*.hcl` manifest
with `--via mv`). Fail-closed: all pairs are validated before anything is
written.

```
tfstackplan state move --dir DIR [flags] <from> <to> [<from> <to> ...]
```

Addresses may be qualified with a stack prefix: `stack:resource_address`.
Unqualified addresses use `--stack` as the default stack.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--stack` | string | `""` | Default stack for unqualified addresses (same-stack moves). |
| `--pr` | string | `""` | PR number for the shim key. Default: `$TFSTACKPLAN_PR`, then git branch name. |
| `--via` | string | `""` | Cross-stack mechanism: `""` (native `import`/`removed` blocks) or `"mv"` (faithful `terraform state mv` via `_tfsp_xmove.*.hcl` manifest). |

### `state import`

The one-sided primitive `state move` composes for the destination side of a
cross-stack move: writes a `moved`-style shim declaring an `import {}` block
for each `<to-addr> <import-id>` pair, with no source-side validation.

```
tfstackplan state import --dir DIR --stack STACK [--pr N] <to-addr> <import-id> [...]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--stack` | string | _(required)_ | Destination stack for the `import {}` block. |
| `--pr` | string | `""` | PR number for the shim key. Default: `$TFSTACKPLAN_PR`, then git branch name. |

### `state remove`

The one-sided primitive `state move` composes for the source side of a
cross-stack move: writes a shim declaring a `removed {}` block for each
resource address given, with no destination-side validation.

```
tfstackplan state remove --dir DIR --stack STACK [--pr N] <addr> [<addr> ...]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--stack` | string | _(required)_ | Source stack for the `removed {}` block. |
| `--pr` | string | `""` | PR number for the shim key. Default: `$TFSTACKPLAN_PR`, then git branch name. |

### `state list`

Prints all pending move shims found under `--dir`.

```
tfstackplan state list --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--pr` | string | `""` | Filter to this PR's moves only. Default: list all. |
| `--all` | bool | `false` | List all moves (the default behavior; flag accepted but has no additional effect). |

### `state moves-manifest`

Discovers all pending move shims and cross-state move manifests under `--dir`
and emits a two-sided `--state-moves` JSON (`{"<stack>":["<addr>",…]}`):
both destination move-ins (planned creates) and source move-outs (planned
destroys). Feed this output to `render --state-moves` so both sides of a
cross-state move classify as moves rather than creates or destroys.

```
tfstackplan state moves-manifest --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--pr` | string | `""` | Filter to this PR's moves only. Default: all pending moves. |
| `-o` | string | `""` | Write JSON to this file. Default: stdout. |

### `state apply`

Discovers every `_tfsp_xmove.*.hcl` manifest under `--dir` and runs each via
`terraform state mv` (pull → back up under `.tfsp-state-backups` → per-pair
decision → `state mv` → push). Dry-run by default; `--execute` performs the
moves. The same executor runs in the `run apply` cross-state-move pre-phase.

```
tfstackplan state apply --dir DIR [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--execute` | bool | `false` | Perform the moves. Default: dry-run (print only). |
| `--lock` | bool | `false` | Acquire a pessimistic GCS state lock around each move. Fail-fast if already locked. Requires ADC. Only active when combined with `--execute`. |

### `state cleanup`

Removes PR-keyed shim files and `_tfsp_xmove.*.hcl` manifests from the tree.
Exactly one of `--pr` or `--all` must be passed.

```
tfstackplan state cleanup --dir DIR (--pr N | --all)
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |
| `--pr` | string | `""` | Remove only this PR's shims and xmove manifests. |
| `--all` | bool | `false` | Remove ALL tfstackplan move shims and xmove manifests in the tree. |

### `state check`

Read-only diagnostic: validates every pending `_tfsp_xmove.*.hcl` manifest
under `--dir` against the local `tfplan.json` files produced by `run plan` —
no terraform invocation, no file mutation. Reports one of `xmove/spent`
(all declared moves already applied), `xmove/valid`, `xmove/source-not-planned`
(source stack has no `tfplan.json` yet), or one or more `xmove/*` errors from
plan validation, per manifest. Also warns on `xmove/data-source-orphan` for
data sources left behind in the source stack. Exits `0` when all manifests
are valid or spent; non-zero if any manifest has an error.

```
tfstackplan state check --dir DIR
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | _(required)_ | Terramate project root. |

---

## `claims`

Inspects and releases apply-lock claims held against a live server.
Requires `TFSTACKPLAN_SERVER`; authenticate to `/api/*` via Google OIDC by
setting `TFSTACKPLAN_AUDIENCE` (the serve URL) with Application Default
Credentials available.

```
tfstackplan claims <subcommand> [flags]
```

Subcommands: `list`, `release`.

### `claims list`

Prints active apply-lock claims for an environment.

```
tfstackplan claims list --env ENV
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--env` | string | _(required)_ | Environment name. |

### `claims release`

Releases one stack's claim, or all claims for a PR, in an environment.

```
tfstackplan claims release --env ENV --pr N [--stack PATH]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--env` | string | _(required)_ | Environment name. |
| `--pr` | int | _(required)_ | PR number whose claim(s) to release. |
| `--stack` | string | `""` | Stack path to release. Omit to release all stacks for the PR. |

---

## `admin`

Operator escape hatches into the server's reconcile core: manually release a
grant, cancel a stuck execution, satisfy a gate, or override a check
conclusion. Each action calls the server via `runner.ClientFromEnv()`, so
`TFSTACKPLAN_SERVER` (and, if configured, `TFSTACKPLAN_AUDIENCE` for OIDC)
must be set.

```
tfstackplan admin <subcommand> <action> [flags]
```

Subcommands: `grants` (action `release`), `executions` (action `cancel`),
`gates` (action `satisfy`), `checks` (action `override`).

### `admin grants release`

Releases a grant.

```
tfstackplan admin grants release --pr N --env ENV --class CLASS --target TARGET [--reason REASON]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--pr` | int | _(required)_ | PR number. |
| `--env` | string | _(required)_ | Environment name. |
| `--class` | string | _(required)_ | Grant class. |
| `--target` | string | _(required)_ | Grant target. |
| `--reason` | string | `"admin intervention"` | Reason for the release. |

### `admin executions cancel`

Cancels an execution.

```
tfstackplan admin executions cancel --id ID [--reason REASON]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | _(required)_ | Execution ID. |
| `--reason` | string | `"admin intervention"` | Reason for the cancellation. |

### `admin gates satisfy`

Satisfies a gate.

```
tfstackplan admin gates satisfy --pr N --env ENV --class CLASS --target TARGET [--reason REASON]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--pr` | int | _(required)_ | PR number. |
| `--env` | string | _(required)_ | Environment name. |
| `--class` | string | _(required)_ | Grant class. |
| `--target` | string | _(required)_ | Grant target. |
| `--reason` | string | `"admin intervention"` | Reason for the satisfy. |

### `admin checks override`

Overrides a check's conclusion.

```
tfstackplan admin checks override --pr N --env ENV --check CHECK --conclusion CONCLUSION [--reason REASON]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--pr` | int | _(required)_ | PR number. |
| `--env` | string | _(required)_ | Environment name. |
| `--check` | string | _(required)_ | Check name. |
| `--conclusion` | string | _(required)_ | Override conclusion. |
| `--reason` | string | `"admin intervention"` | Reason for the override. |

---

## `catalog`

Emits the terramate stack catalog/DAG as JSON: stacks grouped into components
(each component contains a list of stacks and watch expressions), plus edges
linking components (`"watch"` or `"dependency"` kind). Used to build the
group/DAG visualizations and by tooling that needs the stack graph without
running a plan.

```
tfstackplan catalog [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dir` | string | `"."` | Terramate project root. |
| `-o` | string | `""` | Output file path. Default: stdout. |

---

## `whoami`

Prints the resolved server URL, OIDC audience, and the authenticated Google
identity (email or subject) that `TFSTACKPLAN_*` credentials would present to
that server — a diagnostic for "why is my request unauthenticated/rejected."

```
tfstackplan whoami [flags]
```

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--env` | string | `""` | Environment name, used to pick a server from config when `$TFSTACKPLAN_SERVER` is unset (optional). |
