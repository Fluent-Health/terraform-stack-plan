# Environment variables

`tfstackplan` reads its runtime context from the environment. This page lists
every variable the binary reads, grouped by the face that consumes it.

Cross-references: [CLI reference](cli.md) ·
[CI integration guide](../guide/09-ci-integration.md) ·
[Deploy guide](install-and-deploy.md#deploy-serve-to-cloud-run).

---

## `run` — CI driver variables

These variables are read by every `run` subcommand (`plan`, `apply`, `verify`,
`register`, `tick`, `step`, `phase`). They are normally set once in the CI job
environment so the orchestrator and each per-stack invocation share the same
context without threading flags through terramate scripts.

The authoritative constant names live in `internal/runner/env.go`.

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `TFSTACKPLAN_SERVER` | No | `""` | Control-plane base URL. **Empty or unset = fully offline**: `run tick`/`run step` are no-ops; the apply gate check passes unconditionally; no HTTP posts are made. |
| `TFSTACKPLAN_TOKEN` | No | `""` | **Legacy** shared bearer secret for `/api/*` (HS256 tokens minted per request). Takes precedence over `TFSTACKPLAN_AUDIENCE` when both are set. |
| `TFSTACKPLAN_AUDIENCE` | No | `""` | Setting this **opts in** to Google OIDC auth: the client authenticates with ID tokens for this audience (normally the serve URL, matching `api_auth { audience }`) minted from Application Default Credentials — GCE/GKE service accounts natively; **Cloud Build** via the IAM Credentials `generateIdToken` API (its metadata server lacks the identity endpoint), which requires the build SA to hold `roles/iam.serviceAccountOpenIdTokenCreator` on itself; humans via `gcloud auth application-default login`. Unset (and no token) = requests go unauthenticated, exactly as before: no ambient credentials are probed and nothing is sent to the server URL beyond the request itself. If set but ADC is unavailable, a warning is printed and requests degrade to unauthenticated (the fail-closed gate check then errors). |
| `TFSTACKPLAN_EXECUTION` | No | auto-generated | Execution id that correlates all events for one plan or apply run. `run plan` and `run apply` generate a random id when unset; set it explicitly (e.g. `TFSTACKPLAN_EXECUTION=$BUILD_ID`) to make phase events emitted before those commands share the same id and appear in the same check run. |
| `TFSTACKPLAN_ENVIRONMENT` | No | `""` | Deployment environment this run targets (e.g. `staging`, `prod`). Determines the check-run name (`plan/<env>`, or the consolidated `terraform/<env>` on an armed serve-as-driver tier) and the approval gate scope. |
| `TFSTACKPLAN_REPO` | No | `""` | Repository in `owner/name` form. Used to build the GitHub check run and commit status. |
| `TFSTACKPLAN_SHA` | No | `""` | Head commit SHA. Used for GitHub commit status and to anchor links in the report. |
| `TFSTACKPLAN_PR` | No | `""` | Pull-request number (integer). Used to correlate approval grants and to key state-move shim files. Read as a string; `state move` falls back to the git branch name when unset. |
| `TFSTACKPLAN_STACK` | No | `""` | Current stack path. Fallback for `run tick --stack` and `run step --stack` when the flag is omitted. Set by the `run plan`/`run apply` orchestrator for each per-stack invocation. |

### Offline behaviour

When `TFSTACKPLAN_SERVER` is empty:

- `run tick` and `run step` are silent no-ops (exit 0).
- `run plan` renders and prints the Markdown report to stdout; no check run is
  posted.
- `run apply`'s gate pre-check passes — nothing gates, apply runs immediately.

A configured but unreachable server degrades the build to "no live progress"
for all paths **except** `run apply`'s gate check, which is fail-closed.

---

## `run apply` — privilege-backed apply

These variables are relevant when `run apply --impersonate-requester` is used.
`run apply` **sets** `GOOGLE_OAUTH_ACCESS_TOKEN` in its own process environment;
it does not read it as input.

| Variable | Direction | Meaning |
|---|---|---|
| `GOOGLE_OAUTH_ACCESS_TOKEN` | **Set by** `run apply` | Short-lived OAuth2 access token minted for the leased PAM requester service account. Set only when `--impersonate-requester` is passed and the gate check returns a non-empty requester. All `terraform` subprocesses inherit it and apply as that elevated identity. Cleared at process exit — not written to disk. |

The CI apply runner's **ambient** credential (Application Default Credentials)
must hold `roles/iam.serviceAccountTokenCreator` on every service account in
the requester pool so `run apply` can mint the token. See
[CI integration — apply identity and privilege-backed deployment](../guide/09-ci-integration.md#apply-identity-and-privilege-backed-deployment).

---

## `serve` — control-plane variables

`serve` does not read named `TFSTACKPLAN_*` secrets directly. Instead, the
`serve {}` config block carries two fields whose **values are env var names**:
`serve` reads the actual secrets from those named variables at startup.

| Config field | Env var read at startup | Required | Meaning |
|---|---|---|---|
| `serve { webhook_secret_env = "…" }` | The name stored in the field (e.g. `TFSTACKPLAN_WEBHOOK_SECRET`) | No | **Legacy** shared bearer secret accepted on `/api/*` (HS256). Kept accepted alongside OIDC while set — the migration posture. When neither this **nor** `api_auth {}` is configured, `/api/*` auth is disabled (local/dev only). ⚠️ The live-viewer routes (`/`, `/pr/*`, `/live/*`) are gated **only** by this secret (30-day view JWTs), so it cannot be dropped until the viewer machinery is replaced — see the `api_auth` notes in [configuration.md](configuration.md). |
| `serve { github_webhook_secret_env = "…" }` | The name stored in the field (e.g. `GITHUB_WEBHOOK_SECRET`) | No | HMAC secret for validating GitHub webhook payloads. If empty, webhook HMAC validation is skipped. |

All other `serve` credentials (GitHub App private key, GCP/PAM, GCS) are
supplied via files or Application Default Credentials — not environment
variables. See
[`examples/serve.tfstackplan.hcl`](../../examples/serve.tfstackplan.hcl) and
[Deploy on Cloud Run](install-and-deploy.md#deploy-serve-to-cloud-run).

---

## `render` — no environment variables

`render` reads no environment variables. All inputs are flags (`--plans-dir`,
`--config`, etc.). See [CLI reference](cli.md).

---

## `state` — subset of `run` variables

`state move` reads `TFSTACKPLAN_PR` to key the shim files it writes (`PR-<n>`).
When unset it falls back to the git branch name (`branch-<name>`) or `local`.
No other `TFSTACKPLAN_*` variables are read by `state` subcommands.

---

## Complete variable index

| Variable | Face | R/W |
|---|---|---|
| `TFSTACKPLAN_SERVER` | run | Read |
| `TFSTACKPLAN_TOKEN` | run | Read |
| `TFSTACKPLAN_AUDIENCE` | run | Read |
| `TFSTACKPLAN_EXECUTION` | run | Read |
| `TFSTACKPLAN_ENVIRONMENT` | run | Read |
| `TFSTACKPLAN_REPO` | run | Read |
| `TFSTACKPLAN_SHA` | run | Read |
| `TFSTACKPLAN_PR` | run, state | Read |
| `TFSTACKPLAN_STACK` | run | Read |
| `GOOGLE_OAUTH_ACCESS_TOKEN` | run apply (`--impersonate-requester`) | Written |
| *(name in `webhook_secret_env`)* | serve | Read |
| *(name in `github_webhook_secret_env`)* | serve | Read |
