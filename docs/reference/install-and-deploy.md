# Install and deploy

Installation options for the `tfstackplan` binary, and deployment notes for
`serve` on Cloud Run.

---

## Install

### `go install` / `go build`

```bash
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest
```

```bash
go build -o tfstackplan ./cmd/tfstackplan
```

### Prebuilt release binaries

Prebuilt binaries for **linux** and **darwin** on **amd64** and **arm64** are
on the [Releases](https://github.com/Fluent-Health/terraform-stack-plan/releases)
page. Download the binary for your platform, verify the `.sha256`, and place
it on your `PATH`.

### Cloud Run container

The release also publishes a **multi-arch distroless container** to GHCR:

```
ghcr.io/<owner>/terraform-stack-plan:<tag>
```

Its entrypoint is `tfstackplan serve`. The binary is fully static (pure-Go
SQLite, no cgo) and embeds its assets, so the image needs no runtime files.
See [Deploy `serve` to Cloud Run](#deploy-serve-to-cloud-run) below.

### asdf

This repo is its own [asdf](https://asdf-vm.com) plugin — the hook scripts
live in `bin/`, so there is no separate plugin repo. The plugin downloads the
prebuilt release binary and verifies its `.sha256`; no Go toolchain is needed
on the target machine.

Platforms: linux/darwin · amd64/arm64.

```bash
# Add the plugin once per machine:
asdf plugin add tfstackplan https://github.com/Fluent-Health/terraform-stack-plan.git

# Install a version:
asdf install tfstackplan latest
# or a pinned version:
asdf install tfstackplan 0.8.1

# Pin the version in .tool-versions:
asdf set tfstackplan 0.8.1
```

asdf does not auto-add the plugin from a `.tool-versions` entry, so each
machine runs `asdf plugin add …` once; `asdf install` thereafter honours the
pinned version.

---

## Deploy `serve` to Cloud Run

`serve` is a single-instance control plane. See [serve](../guide/07-serve.md)
for what it does, [environment](environment.md) for all env/config variables,
and [cli](cli.md) for the full flag reference.

### Image

```
ghcr.io/<owner>/terraform-stack-plan:<tag>
```

Built by the release workflow. Entrypoint: `tfstackplan serve`. Default listen
address: `:8080` (override with `--addr`).

### Configuration

Mount a `.tfstackplan.hcl` containing at minimum a `serve {}` block (with
`db_path`, `public_base_url`, `github_app {}`, and `approval "gcp-pam" {}`),
`class "<name>" {}` bindings for each approval gate, and a `classification {}`
block matching your stack layout. Pass the file path via `--config` or place it
where auto-discovery can find it.

See [`examples/serve.tfstackplan.hcl`](../../examples/serve.tfstackplan.hcl)
for the full commented reference.

### Single instance + durability

Set Cloud Run **min instances = max instances = 1**. The SQLite store is
single-writer by design (WAL mode + `busy_timeout` set at the DSN level); more
than one instance is not supported.

Persist and replicate the database with **Litestream**: run Litestream alongside
the binary (a wrapper entrypoint or sidecar), replicating the path given in
`serve { db_path }` to a GCS bucket. On restart, Litestream restores from the
bucket before the server starts. Executions are ephemeral, so any data loss
between replications is cosmetic — Litestream is the DR story.

### Logs and GCS offload

Set `serve { logs_dir }` to a writable path for per-stack log buffers. Add an
`objects { backend = "gcs", bucket = "…", prefix = "…" }` block to offload
completed-stack logs to GCS; the server stores a pointer and serves log content
back through itself, so viewers require no cloud IAM.

### Pub/Sub push ingestion

Add a `serve { pubsub { audience = "…", service_account = "…" } }` block to
enable OIDC-verified push ingestion as a latency improvement over the poll
loop.

### Identity

Run the Cloud Run service as a dedicated service account with:

- PAM viewer role, plus a revoke role on the target projects.
- `serviceAccountTokenCreator` on each identity in the requester pool
  (`serve { approval "gcp-pam" { requester_pool = […] } }`).

Mount the GitHub App private key as a Secret Manager–backed secret file; set
the path in `github_app { private_key_path }`. Set the bearer secret via the
env var named in `serve { webhook_secret_env }`.

See `SECURITY.md` for the full IAM reference.
