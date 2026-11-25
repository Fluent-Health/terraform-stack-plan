# Rust port of tfstackplan

`tfstackplan` is moving from Go to Rust one subcommand at a time. This directory
holds the Rust side: a front door binary, a replay harness that checks it
against the Go binary, and recorded cases.

`serve` runs in Rust (crates `server/`, `server-clients/`, on top of `gate-core/` and
`store/`). Every other subcommand still runs in Go. `TFSTACKPLAN_IMPL_SERVE=go` sends
`serve` back to Go.

## The front door

`tfstackplan` (crate `tfstackplan/`) looks at the first argument to pick the
subcommand, the same way Go does: a leading flag or no arguments means
`render`. If the subcommand is in the `PORTED` table in `src/main.rs`, it runs
in Rust. Otherwise the front door runs the Go binary with the same argv, env,
stdin, stdout and stderr, and exits with its exit code. On unix it replaces its
own process with Go (`exec`), so signals, the tty and the exit code all belong
to Go. On other platforms it spawns Go, waits and passes the exit code on.

The front door does not parse flags. Go owns its flags until a subcommand is
ported.

| Variable | Effect |
|---|---|
| `TFSTACKPLAN_GO_BIN` | Path to the Go binary. Default: `tfstackplan-go` next to the front door executable. If neither exists the front door prints one error and exits 1. |
| `TFSTACKPLAN_IMPL=go` | Kill switch: send every subcommand to Go. |
| `TFSTACKPLAN_IMPL_<SUBCOMMAND>=go` | Send one subcommand to Go, for example `TFSTACKPLAN_IMPL_RENDER=go`. |
| `TFSTACKPLAN_FALLBACK_LOG` | If set to a file path, append one line with the subcommand name each time the front door falls back to Go. |

## Building locally

You need the Go toolchain from the repo root and Rust 1.98.1 (pinned in
`rust-toolchain.toml`; rustup installs it on first use), plus
[cargo-nextest](https://nexte.st) and
[cargo-deny](https://github.com/EmbarkStudios/cargo-deny) for the checks below.

```bash
# from the repo root
CGO_ENABLED=0 go build -o rust/.go/tfstackplan-go ./cmd/tfstackplan

cd rust
cargo build --release
cp .go/tfstackplan-go target/release/   # the front door finds it next to itself
target/release/tfstackplan -version
```

`rust/.go/` and `rust/target/` are gitignored.

## Before you push

Run the same checks as the CI `rust` job, from `rust/`, and confirm each exits 0:

```bash
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --locked -- -D warnings
RUSTDOCFLAGS='-D warnings' cargo doc --workspace --no-deps --locked
cargo nextest run --profile pr --workspace --locked
cargo test --doc --workspace --locked
cargo deny --locked check advisories bans licenses sources
cargo run -p recorder && git diff --exit-code -- cases   # recordings still match Go
```

Unit tests live in a `tests.rs` beside the module (`#[cfg(test)] mod tests;`), not
in an inline `mod tests { … }` block.

## Replay cases

Cases live in `cases/` in [trycmd](https://docs.rs/trycmd) format:

- `<name>.toml` holds `bin.name`, `args` and the working directory (`fs.cwd`,
  relative to `cases/`). Most cases run from the repo root and point at fixtures
  in place, such as `internal/config/testdata/*.hcl` and `examples/.tfstackplan.hcl`.
- `<name>.stdout`, `<name>.stderr` and `status.code` in the toml are the recorded
  Go output.
- Cases that write files (`--output`, `--emit-classification-json`) set
  `fs.sandbox = true` with `fs.base = "fixtures"`; trycmd copies that directory
  to a temp dir, runs there, and compares the files in `<name>.out/`.
- `cases/fixtures/` holds plan trees: `small/` (the plan fixtures from
  `internal/plan/testdata`), and `big/` and `state-ops/` (the inputs behind the
  `examples/*.md` goldens), plus `policy.hcl` for the sandboxed cases.

To add a case, write the `.toml` with `bin.name`, `args` and `fs.cwd`, then
record it.

### Record

The recorder (crate `recorder/`) runs the Go binary on every case twice and
writes the expected outputs. Where the two runs differ, such as a timestamp,
it writes a `[..]` wildcard for that part of the line.

```bash
cd rust
TFSTACKPLAN_GO_BIN=$PWD/.go/tfstackplan-go cargo run -p recorder            # all cases
TFSTACKPLAN_GO_BIN=$PWD/.go/tfstackplan-go cargo run -p recorder -- version # one case
```

### Replay

```bash
cd rust
cargo nextest run   # uses TFSTACKPLAN_GO_BIN, else rust/.go/tfstackplan-go
cargo test -p tfstackplan-server --test replay   # serve: Go and Rust side by side
```

The serve replay starts Go's `serve` and the Rust `serve` with the same config against a
fake GitHub (`TFSTACKPLAN_GITHUB_API_URL`), sends the same requests to both, and compares
status, headers, bodies, GitHub calls and every table. It writes
`cases/serve-replay-report.md`.

`tests/replay.rs` runs the front door against every case and writes
`cases/replay-report.md` with cases passed and how many invocations fell back to
Go. The report is committed. With nothing ported, the expected result is every
case passing with every invocation falling back.

CI (`rust` job in `.github/workflows/ci.yml`) runs the checks in *Before you push*,
then re-records the cases and fails if `rust/cases` changed. If a Go change
alters output, re-record in the same PR.

See [DECISIONS.md](DECISIONS.md) for why it is built this way.
