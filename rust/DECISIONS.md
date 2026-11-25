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


## Static musl on Linux, built with cargo-zigbuild

Release archives and both images ship the front door and `tfstackplan-go` side
by side. On Linux the front door targets `*-unknown-linux-musl`, so it is a
static binary and runs on `gcr.io/distroless/static-debian12`, the same base the
static Go binary already uses. The alternative was a glibc build on
`distroless/cc`, which adds libc and libgcc to the image and gives Cloud Run and
the runner image a second base to track, for no gain while the front door has
no C dependencies. musl's allocator is slower than glibc's; that matters only
once a hot path is ported, and a Rust allocator crate fixes it then.

The release workflow builds every target on one Linux runner with
cargo-zigbuild, using zig as the linker. Pure Rust needs zig only to link, but
bundled SQLite (rusqlite with `bundled`) compiles C, and a musl or macOS target
then needs a C cross toolchain. zig is that toolchain, so a port that adds
SQLite keeps the same build, without musl-gcc or a macOS SDK.
