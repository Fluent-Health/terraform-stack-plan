# Security

`tfstackplan serve` is a long-running control plane that holds a GitHub App
private key, authenticates `/api/*` via Google OIDC, and requests
time-bound privileged-access grants. It executes no Terraform and holds no
apply credentials — Terraform runs in your own CI under your own identities.

## Reporting

Report vulnerabilities privately via a GitHub security advisory on this repo.
Do not open public issues for security reports.

## Operator guidance

- **API auth is Google OIDC, not a shared secret.** `/api/*` is authenticated
  by Google OIDC: `serve { api_auth {} }` declares the accepted token
  audiences and an identity→scope allowlist, and the server verifies the
  id-token signature and audience, then maps the verified email to scopes.
  There is no shared bearer secret; the old HS256 shared-secret path has been
  removed. Auth is disabled only when `api_auth {}` is not configured — local/dev
  only, never production.
- **Secrets are mounted, never baked.** The GitHub App private key
  (`serve.github_app.private_key_path`) is read from a mounted file at
  runtime — never built into the image or committed. Use your platform's
  secret store (e.g. a mounted secret volume).
- **`serve.github_webhook_secret_env` is the per-tier gate, not optional
  hardening.** It is the sole check on that tier's own `/github/webhook`. When
  unset, the endpoint 404s outright — no HMAC verification runs because the
  route doesn't exist — which disables GitHub-driven functionality for that
  tier: PR-lock handling, run-triggering (plan/apply), merge_group evaluation,
  and check_run/check_suite re-run. Set it per tier for that functionality to
  work.
- **`ui.github_webhook_secret_env` is optional, additional hardening.** It is
  a separate config field on the central UI's relay: when set, the relay
  verifies GitHub's HMAC before fanning a delivery out to each tier, so
  garbage dies at the relay instead of reaching every tier. It is
  defense-in-depth on top of, not a substitute for, each tier's own
  `serve.github_webhook_secret_env` check.
- **GitHub App key rotation.** Rotate the App private key in GitHub, update the
  mounted file, and restart. Tokens are minted per request and short-lived, so a
  rotated key takes effect immediately on restart.
- **Approval-event ingestion (when enabled).** A provider push endpoint must
  verify the caller (e.g. OIDC token audience + issuer) before acting on an
  event. The current build satisfies gates via a polling reconcile loop and does
  not expose a push endpoint; if you add one, verify it.
- **Least privilege (gcp-pam).** The server runtime identity needs only PAM
  viewer + a revoke role on the targets and `serviceAccountTokenCreator` on the
  requester pool SAs. It can request and revoke grants; it cannot approve (PAM
  blocks self-approval and the server holds only a requester identity).
- **Privilege-backed apply (`--impersonate-requester`).** When the flag is set,
  `run apply` calls the IAM Credentials `generateAccessToken` API using the
  **CI runner's** ambient ADC, which must hold `roles/iam.serviceAccountTokenCreator`
  (or `iam.serviceAccounts.getAccessToken`) on every SA in the requester pool. The
  minted access token is set as `GOOGLE_OAUTH_ACCESS_TOKEN` in the `run apply`
  process and inherited by the `terraform` subprocess; it is not written to disk
  or transmitted beyond the apply run (process-scoped, one-shot). The server
  itself only ever *requests* grants (humans approve in GCP) and uses its own
  ambient ADC for list/revoke — it never sees or handles the CI runner's token.
  When `run apply` cannot mint the token, it fails closed (exit 1) and no stack
  is applied. When the gate check returns no requester (clean/gateless plan, or
  no server configured), the flag is silently a no-op — no token is minted and
  apply proceeds under the ambient CI identity, which is correct because no
  elevated permission is required.
- **Single instance per environment.** The store is single-writer (SQLite +
  Litestream). Run exactly one instance per environment; horizontal scaling is
  unsupported by design (this is a control plane, not a data plane).
- **Read surface.** `/live` and `/img` are public behind unguessable execution
  ids (same sensitivity class as plan output already posted to PRs). Put them
  behind your own auth proxy (e.g. IAP) if you need access control.
