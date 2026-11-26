# Rust port of tfstackplan

`tfstackplan` is moving from Go to Rust one subcommand at a time. This directory
holds the Rust side: a front door binary, a replay harness that checks it
against the Go binary, and recorded cases.

Ported to Rust: `render` (including the legacy form with a leading flag or no
arguments), `state`, `claims`, `catalog`, `uniqueness`, `whoami`, `admin`, and
`run wrap` (with `run claims` and `run whoami`, which are the same faces).
Everything else, including the rest of `run`, still runs in Go.

| Crate | Package | What it holds |
|---|---|---|
| `render-core/` | `tfstackplan-render` | The pure render pipeline, no I/O, clock or async. One module per Go package under `internal/` (`plan`, `moveset`, `model`, `classify`, `differ`, `render`, `fit`, `links`, `ansi`, `codes`, `source`, and the render part of `config`), plus `gofmt`, `goval`, `gojson`, `goregex`, `difflib` and `yaml`, which reproduce the Go library behaviour the output depends on. |
| `cli-core/` | `tfstackplan-cli` | The other faces' logic, pure: no I/O, no clock, not async. Go-compatible helpers (`flag`, `filepath.Match`, URL escaping), config parsing, state-move planning and the shim/xmove HCL, uniqueness, catalog. |
| `clients/` | `tfstackplan-clients` | Blocking HTTP clients with connect and total timeouts: the control-plane server, GCS (state lock, objects), Google credentials (ADC, service-account keys, impersonation, the metadata server). |
| `tfstackplan/` | `tfstackplan` | The binary: the front door and each face's shell (flags, files, terraform/terramate, printing). `src/render.rs` is `render`'s shell (`goflag.rs`, `gofs.rs`). |
| `casekit/` | `casekit` | What the recorder and the replay share: the hermetic environment and the fake server. |
| `recorder/` | `recorder` | Records the replay cases from the Go binary. |

## The front door

`tfstackplan` (crate `tfstackplan/`) looks at the first argument to pick the
subcommand, the same way Go does: a leading flag or no arguments means
`render`. If the subcommand is in the `PORTED` table in `tfstackplan/src/lib.rs`
(or, for `run`, its second argument is in `PORTED_RUN`), it runs in Rust.
Otherwise the front door runs the Go binary with the same argv, env,
stdin, stdout and stderr, and exits with its exit code. On unix it replaces its
own process with Go (`exec`), so signals, the tty and the exit code all belong
to Go. On other platforms it spawns Go, waits and passes the exit code on.

The front door does not parse flags for subcommands it passes on. A ported
subcommand parses its own flags exactly as Go's `flag` package does, so usage
text, error messages and exit code 2 match Go's.

`-version` prints `tfstackplan dev`, or the value of `TFSTACKPLAN_VERSION` at
build time (the Go release sets `main.version` the same way).

| Variable | Effect |
|---|---|
| `TFSTACKPLAN_GO_BIN` | Path to the Go binary. It must be a path (absolute, or with a `/`), not a bare name, and must not be the front door itself. Default: `tfstackplan-go` next to the front door executable, with symlinks to the front door resolved first. |
| `TFSTACKPLAN_IMPL=go` | Kill switch: send every subcommand to Go. The value is case-insensitive. |
| `TFSTACKPLAN_IMPL_<SUBCOMMAND>=go` | Send one subcommand to Go, for example `TFSTACKPLAN_IMPL_STATE=go`. For `run wrap` the name is `RUN`. |
| `TFSTACKPLAN_FALLBACK_LOG` | If set to a file path, append one line with the subcommand name each time the front door hands a call to Go (after it found the Go binary). |

When it cannot hand a call to Go, the front door prints one line to stderr and
exits like a shell would: **127** if the Go binary is not found (or
`TFSTACKPLAN_GO_BIN` is a bare name), **126** if it is found but cannot be run
(not executable, or it is the front door itself). Otherwise the exit code is Go's.

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
cargo run -p recorder && git diff --exit-code -- cases replay-report.md   # recordings still match Go
```

Unit tests live in a `tests.rs` beside the module (`#[cfg(test)] mod tests;`), not
in an inline `mod tests { … }` block.

## Replay cases

Cases live in `cases/` in [trycmd](https://docs.rs/trycmd) format:

- `<name>.toml` holds `bin.name`, `args` and the working directory (`fs.cwd`,
  relative to `cases/`). Most cases run from the repo root and point at fixtures
  in place, such as `internal/config/testdata/*.hcl` and `examples/.tfstackplan.hcl`.
- Every case runs with a cleared environment, so its toml carries
  `env.inherit = false` (the recorder adds it). A case that needs a variable sets
  it under `[env.add]`. The run gets only `PATH`, an empty `HOME`, the Go binary
  and the fallback log on top.
- `wildcards = true` (top level, optional) lets the recorder write `[..]` where
  Go's output varies between runs. Without it any variation fails the recording.
- `<name>.stdout`, `<name>.stderr` and `status.code` in the toml are the recorded
  Go output.
- Cases that write files (`--output`, `--emit-classification-json`) set
  `fs.sandbox = true` with `fs.base = "fixtures"`; trycmd copies that directory
  to a temp dir, runs there, and compares the files in `<name>.out/`.
- `cases/fixtures/` holds plan trees: `small/` (the plan fixtures from
  `internal/plan/testdata`), `big/` and `state-ops/` (the inputs behind the
  `examples/*.md` goldens), and `links/` with its source tree `links-src/` (`.tf`
  files, a local module and `.terraform/modules/modules.json`) for resource
  `{file}#L{line}` links. Beside them are `policy.hcl` for the sandboxed cases and
  `prod-like.hcl`, a policy file shaped like a production one (links, cache,
  progress and `env_uniqueness` blocks) with made-up names.

To add a case, write the `.toml` with `bin.name`, `args` and `fs.cwd`, then
record it. A case must not write into the repository: one that writes files
(`--output`, `--emit-classification-json`) goes in a sandbox.

**Format change for branches made before this one:** run the recorder once after
rebasing. It adds `env.inherit = false` to each case and re-records it with the
cleared environment. The report moved from `cases/replay-report.md` to
`replay-report.md`.

### Environment, fake servers and tools

On top of the cleared environment, `HOME` is a fresh empty directory per run,
and the variables that point the binary at servers or credentials
(`TFSTACKPLAN_SERVER`, `TFSTACKPLAN_AUDIENCE`, `GOOGLE_APPLICATION_CREDENTIALS`,
`GCE_METADATA_HOST`, … see `casekit::HERMETIC_UNSET`) are set empty, so neither
binary sees a developer's real credentials. A case can add a `[harness]` table,
which trycmd ignores:

```toml
[harness]
requires = ["terraform"]                    # skip when not on PATH
env = { TFSTACKPLAN_SERVER = "{fake}" }     # {fake}: fake server URL, {fakehost}: host:port
adc = "authorized_user"                     # ADC file in HOME, token_uri on the fake server

[[harness.route]]                           # the fake server's canned answers
method = "POST"
path = "/api/claims/list"
status = 200
body = '[]'
```

A case with routes or `adc` gets a fake HTTP server on 127.0.0.1 for each run.
Its URL differs per run, so recordings show it as `[FAKE]` (`[FAKEHOST]` for the
bare host:port). With `adc`, the server also answers `POST /token` with an
access token and an unsigned ID token for `ci-bot@example.com`.

Cases with `requires` are skipped, with a message, when the program is not on
PATH: the recorder leaves their recording alone and the replay lists them under
*Skipped* in the report. `state apply` cases need terraform 1.13.3 and the
`catalog` cases terramate 0.17.x (the versions in `.tool-versions`); CI installs
both.

The recorder also applies a case's trycmd `[env] add` to the Go runs, widens a
`[..]` wildcard to the whole run of letters and digits that differs (random ids,
temp-dir suffixes), and records written files with `\` turned into `/`, because
trycmd normalises path separators in the files it compares.

### Record

The recorder (crate `recorder/`) runs the Go binary on every case three times,
with the cleared environment and the case's `env.add`, and writes the expected
outputs. The three runs must agree. In a case with `wildcards = true`, a line
that differs keeps its common prefix and suffix with `[..]` between, and only if
the varying part is at most 40 characters. `.out/` cannot hold a deletion, so
base files a sandboxed run deletes are listed in the toml as `deletes`.

```bash
cd rust
TFSTACKPLAN_GO_BIN=$PWD/.go/tfstackplan-go cargo run -p recorder            # all cases
TFSTACKPLAN_GO_BIN=$PWD/.go/tfstackplan-go cargo run -p recorder -- version # one case
```

### Replay

```bash
cd rust
cargo nextest run   # uses TFSTACKPLAN_GO_BIN, else rust/.go/tfstackplan-go
```

`tests/replay.rs` runs the front door against every case through trycmd (stdout,
stderr, exit code, `.out/` contents) and then checks what trycmd does not:

- the case's fallback log has exactly one line when the subcommand should run in
  Go (not ported, or a kill switch in its `env.add`) and none when it should run
  in Rust, so a ported subcommand cannot silently pass by running Go;
- a sandboxed case leaves exactly its base files, less its `deletes`, plus its
  `.out/` files, so an extra or unexpectedly deleted file fails;
- a case that is not sandboxed leaves the checkout unchanged.

It writes `replay-report.md` with cases passed and how many invocations fell back
to Go. The report is committed. Every case must pass; the ported faces answer in
Rust and the rest fall back to Go.

### Go parity property test

`tests/go_parity.rs` mutates the fixture plans at random (attribute values,
including JSON and YAML strings, numbers and Unicode; sensitivity and
known-after-apply markers; actions; dropped changes), renders each mutation with
the Go binary and with the front door under random configs, budgets and
`--details`, and requires identical stdout, stderr, exit code and
classification JSON. Plans are written with object keys in sorted or reversed
order, and every run resolves resource links against the `.tf` tree in
`cases/fixtures/links-src/` (the `links` fixture set always has links on). It is
skipped, with a loud note, when the Go binary is absent, and fails instead when
`CI` is set.
`PROPTEST_CASES` sets the number of cases (default 96).

CI (`rust` job in `.github/workflows/ci.yml`) runs the checks in *Before you push*,
then re-records the cases and fails if `rust/cases` changed, and fails separately
if `replay-report.md` changed. If a Go change alters output, re-record in the
same PR.

See [DECISIONS.md](DECISIONS.md) for why it is built this way.
