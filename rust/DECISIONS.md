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
exits with Go's code. That path does not forward signals.

## Where the Go binary is found

`TFSTACKPLAN_GO_BIN` wins, so tests, CI and unusual installs can point anywhere.
Otherwise the front door looks for `tfstackplan-go` in its own directory, which
is how a release would ship the two binaries side by side. There is no `PATH`
search: finding some other `tfstackplan` on `PATH` could be the front door
itself and loop. A missing binary is one clear error and exit 1.

## Replay cases use trycmd

trycmd cases are plain files (`.toml` for args, `.stdout`/`.stderr` for output,
`.in`/`.out` directories for files), so a recording is easy to review in a diff
and easy to regenerate. trycmd already handles sandboxes, exit codes and `[..]`
wildcards, which is all render needs. Our own recorder fills the files from the
Go binary rather than from the Rust binary (trycmd's own overwrite mode), so the
recording is always Go's behavior and Rust has to match it.

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
`null` and read as empty.

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
the Go test `TestWriteRustCompatDB`). Every stored event and snapshot decodes and re-encodes to
the same JSON, every stream replays to the state Go snapshotted, and rebuilding the projections
from the log reproduces Go's tables. Two columns are not functions of any log and are exempt:
an execution's `failure` after a failed start, which Go writes from that round's events and a
later round can overwrite, and a stack's last `gated`/`safe` overlay, which stays after its gate
is released.
