# Decisions

## Dispatch on the first argument only

The front door reads the first argument and nothing else. A leading flag or no
arguments counts as `render`, as in Go's `dispatch`. A ported subcommand runs in
Rust; everything else goes to Go untouched. Parsing flags in the front door would
mean keeping two copies of every flag set in sync, and Go already gets them right.
A subcommand's flags move to Rust together with that subcommand.

## exec on unix, spawn elsewhere

On unix the front door replaces itself with the Go binary through
`CommandExt::exec`. Go then owns the pid, the controlling tty (the `wrap` face
runs under a pty), signal delivery and the exit code, so nothing has to be
forwarded and a fallback behaves exactly like running Go directly. Windows has
no exec, so there the front door spawns Go with inherited stdio, waits, and
exits with Go's code. That path does not forward signals. On Windows, Ctrl-C
reaches both processes in the console; the front door does not handle it, so it
may exit before Go finishes cleaning up. This is left as is until the front door
ships on Windows in earnest.

## Where the Go binary is found

`TFSTACKPLAN_GO_BIN` wins, so tests, CI and unusual installs can point anywhere.
Otherwise the front door looks for `tfstackplan-go` in its own directory, which
is how a release would ship the two binaries side by side. There is no `PATH`
search: finding some other `tfstackplan` on `PATH` could be the front door
itself and loop. For the same reason `TFSTACKPLAN_GO_BIN` must be a path, not a
bare name (exec would search `PATH` for a name the front door had checked against
the working directory), and the front door execs exactly the path it checked and
refuses one that resolves to itself. The default lookup resolves symlinks to the
front door first, so a `tfstackplan` linked onto `PATH` (macOS does not resolve
`current_exe` for us) still finds the `tfstackplan-go` installed beside it.
Failures use the shell's codes: 127 not found, 126 not runnable.

## Replay cases use trycmd

trycmd cases are plain files (`.toml` for args, `.stdout`/`.stderr` for output,
`.in`/`.out` directories for files), so a recording is easy to review in a diff
and easy to regenerate. trycmd already handles sandboxes, exit codes and `[..]`
wildcards, which is all render needs. Our own recorder fills the files from the
Go binary rather than from the Rust binary (trycmd's own overwrite mode), so the
recording is always Go's behavior and Rust has to match it.

The replay adds the checks trycmd lacks (fallback log per case, exact sandbox
file set, untouched checkout), and both the recorder and the replay run with a
cleared environment, so a variable on a developer's machine cannot change a
recording or let a case pass. Wildcards are opt-in per case and capped in length,
and the recorder takes three samples, so `[..]` cannot quietly cover a real
difference.

## Conventions follow the organisation's other Rust services

The toolchain (1.98.1), the workspace lint baseline, `clippy.toml`, `deny.toml`, the build
profiles and the nextest `pr` profile are the same as in the organisation's other Rust services.
One toolchain and one lint and dependency policy means a reviewer who knows one of those
services can read this one, and a toolchain or lint bump moves all of them together. The lint
baseline denies `unsafe`, `unwrap` and leftover `dbg!`/`todo!`, and every lint silenced in code
says why with `#[expect(..., reason = "...")]`. `deny.toml` allows only rustls for TLS, so the
binary keeps running on a distroless image with no libssl. CI runs on GitHub Actions because
this repository is public, with the same commands the other services run.

## Crate layout

Ported code is split by how often it changes and whether it does I/O. The first port, `render`,
goes in a core library crate that does no I/O, reads no clock and is not async: plans in,
markdown out. That keeps it testable case by case against the Go binary. Code that talks to
GitHub, GCS or terraform goes in a separate clients crate. The `tfstackplan` binary stays the
thin shell that parses arguments, reads files and wires the two together.


## The serve core: `tfstackplan-gate` and `tfstackplan-store`

Serve's state moves in two crates. `tfstackplan-gate` (`gate-core/`) is the pure core: the
gate (`reconcile`), execution and claim-ledger deciders, the runner wire types and their
codecs. No I/O, no clock (a claim's `now` is an argument), no async. `tfstackplan-store`
(`store/`) is SQLite: the event log, snapshots, the projections and one decide, fold, persist
and project round per aggregate. The HTTP layer, the approval backend and the actions that
call GitHub or the executor will sit on top and hold a key's `KeyedLocks` lock across a round
and its actions, as Go's shell holds its per-(pr, environment) mutex.

The deciders keep Go's shape and names: `decide`, `evolve`, `react`, and the same event,
signal and action names, so the two can be read side by side. Each event is an enum variant
wrapping a struct of the same name, which is what the codec serializes, and the struct's JSON
field names are Go's (`ExecutionID`, `ByPR`, and so on). Go's nil slices and maps arrive as
`null` and read as empty. A grant state outside the normalised set, which Go's PAM backend
passes through unchanged, reads as `GrantState::Other` and is written back as it came: closed,
ranked with the terminal states, and never a block.

## SQLite through rusqlite with the bundled library

`rusqlite` with `bundled` compiles SQLite into the binary, so there is no system library to
match and every build runs the same SQLite. It needs a C compiler at build time (the
`rust:*-slim` images have gcc). It is pinned exactly because a rusqlite release moves the
bundled SQLite with it. The store holds one connection behind a mutex: serve is one process,
so writers in that process queue on the mutex.

Two settings keep Rust and Go writing the same database the same way:

- Every write is an explicit `BEGIN IMMEDIATE` transaction, Go's `_txlock=immediate` fix for
  stack ticks lost to `SQLITE_BUSY`. A busy handler polls the lock every millisecond for up to
  five seconds. Go's `busy_timeout(5000)` has the same limit, but SQLite's own timeout backs off
  to 100 ms between tries, and under contention a waiting connection starved past it.
- Foreign keys are off. The bundled SQLite turns them on by default; Go's driver does not, and
  the schema's references were never enforced.

## Migrations and goose's version table

The store embeds `internal/store/migrations/*.sql` (the same files Go embeds) and applies the
`-- +goose Up` part of each one newer than the database's version, in its own transaction
together with a `goose_db_version` row, the way goose does. It creates and reads that table as
goose does, so a database either binary migrated opens in the other without re-running
anything. A test fails if a migration file is added without being listed, or if one uses a
goose annotation this runner does not implement.

## Compatibility with the Go database

`store/tests/go_compat.rs` opens a database Go's shell wrote (`fixtures/go.db`, regenerated by
the Go test `TestWriteRustCompatDB`, which emits every event kind). Every stored event and
snapshot decodes and re-encodes to the same JSON, every stream replays to the state Go
snapshotted, and rebuilding the projections into empty tables gives Go's rows. The rebuild
replays each stream one event at a time and projects after each, as the rounds did, because
some of what a round writes is not in the final fold: a progress label a later phase leaves
alone, the last overlay of a released gate. One value is exempt: a serve-started execution
whose start failed and that a later supersede re-projected, because which of the two streams
moved last is in neither log. CI regenerates the database from the Go code under review before the
tests run, so the committed copy is only the fallback for local runs.

Timestamps are the one place the bytes differ. Go writes a `time.Time` in JSON with
the offset it was created with (`+05:30`, or `Z` for UTC); jiff always writes UTC
with `Z`. Both read either form, and the instant is the same. In SQLite, a claim's
`expires_at` is compared as text, so the store writes Go's `time.String()` form for
UTC there, never jiff's.
