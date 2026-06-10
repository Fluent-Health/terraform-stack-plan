# Deploying `tfstackplan serve` to Cloud Run

`serve` is a single-instance control plane. Deploy the released container.

## Image

`ghcr.io/<owner>/terraform-stack-plan:<tag>` (built by the release workflow).
Its entrypoint is `tfstackplan serve`; it listens on `:8080` (override with
`--addr`).

## Configuration

Mount a `.tfstackplan.hcl` with a `serve {}` block (db path, public base URL,
`github_app`, `approval "gcp-pam"`) and `class "<name>" {}` bindings, plus the
`classification {}` rules. See `docs/DESIGN.md` for the schema.

## Single instance + durability

- Set Cloud Run **min instances = max instances = 1** (single SQLite writer).
- Persist the DB and replicate it with **Litestream** to a bucket: run
  Litestream alongside the binary (a wrapper entrypoint or sidecar) replicating
  `serve.db_path`. On restart, Litestream restores from the bucket. Executions
  are ephemeral, so data loss is cosmetic; Litestream is the DR story.

## Identity

Run as a service account with: PAM viewer + a revoke role on the target
projects, and `serviceAccountTokenCreator` on the requester-pool SAs. Mount the
GitHub App private key as a secret file and set the bearer secret via the
configured env var. See `SECURITY.md`.
