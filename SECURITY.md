# Security

`tfstackplan serve` is a long-running control plane that holds a GitHub App
private key and a bearer secret, and requests time-bound privileged-access
grants. It executes no Terraform and holds no apply credentials — Terraform runs
in your own CI under your own identities.

## Reporting

Report vulnerabilities privately via a GitHub security advisory on this repo.
Do not open public issues for security reports.

## Operator guidance

- **Secrets are mounted, never baked.** The GitHub App private key
  (`serve.github_app.private_key_path`) and the bearer secret
  (`serve.webhook_secret_env`) are read from a mounted file / environment
  variable at runtime — never built into the image or committed. Use your
  platform's secret store (e.g. a mounted secret volume).
- **Bearer rotation.** `/api/*` mutations require `Authorization: Bearer
  <secret>`. Rotate by updating the secret store and restarting; an empty secret
  disables auth and is for local/dev only — never production.
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
