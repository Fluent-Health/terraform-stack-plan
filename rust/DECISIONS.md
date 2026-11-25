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


## Render: one pure core crate, Go's behaviour reproduced in small modules

`render` is ported into `render-core` (package `tfstackplan-render`) as plain functions over
bytes and strings; the binary does every read and write and calls it in Go's order, so the
first error is the one Go reports. Where the output depends on Go library behaviour, a small
module reproduces it instead of reaching for a crate that is close but not equal:
`gofmt` (strconv float layout and `%q` quoting, using Go's own `IsPrint` tables), `goval` and
`gojson` (what `encoding/json` decodes into `any`, how it encodes with HTML escaping, and its
exact syntax error messages), `goregex` (Go's ASCII-only `\d`, `\w`, `\s`, `\b` on top of
`regex`, and Go's error wording), `difflib` (the go-difflib port of Python's SequenceMatcher)
and `yaml` (yaml.v3's scalar resolution and its libyaml emitter). There is no `terraform-json`
crate, so plan JSON is read through `serde_json` into those Go-shaped values.

## HCL is parsed with hcl-rs

`hcl-rs` (pinned) parses the policy file and `.tf` sources; its re-exported `hcl-edit` keeps
byte spans, which give hcl's `file:line,col-col: Summary; Detail` diagnostics for decode
errors (unsupported or missing arguments, wrong labels, unsuitable value types). It accepts
every config in `internal/config/testdata` and `examples/`. YAML syntax is parsed by
`yaml-rust2`, a libyaml-style parser like the one inside yaml.v3; resolution and decoding on
top of its events follow v3.

## Fit keeps Go's choices but not its cost

Go's fit degrades one attribute at a time and re-renders the whole report after every step,
which is quadratic. The port makes the same choices in the same order (largest selected
variant first, earliest on ties) but keeps the size current incrementally: a step changes one
resource row, and a row's share of the document depends only on its own text. A test-only
port of the Go loop and a property test check the two give identical reports, and the Go
parity test covers it end to end. On a 20-stack by 50-resource plan set with the default
budget, Go takes about 11 s and the port about 70 ms.

## Go quirks kept on purpose

These are Go's behaviour, kept so output stays byte-identical:

- yaml.v3 output never folds long lines, and escapes every character above U+FFFF (emoji
  included) in double quotes, because libyaml's printable check only knows 1 to 3 byte UTF-8.
- An unquoted YAML date such as `2024-01-31` decodes to a timestamp and prints as
  `2024-01-31T00:00:00Z`; `0777` is octal; `08` is the float 8.
- The diff filter drops any diff line starting with `---` or `+++`, including a content line
  such as a removed `--` YAML separator.
- `difflib.SplitLines` adds a final empty line, so a structural diff ends with a lone ` `
  context line.
- Map key order in YAML output is yaml.v3's natural order (`a2` before `a10`); in JSON output
  it is plain byte order.

## A closed stdout exits 141

Go's runtime dies of SIGPIPE when a write to a closed stdout fails, so a shell sees 141. Rust
ignores SIGPIPE and returns the write error instead, and putting the default handler back
needs unsafe code, which the crate forbids. So `render` (including `-version`) exits with 141
itself on a broken stdout pipe and writes nothing else: the same status for a shell or
`set -o pipefail`, but a parent calling `waitpid` sees a normal exit, not a signal. Other
stdout errors are ignored, as `fmt.Print` ignores them.

## Parity is with Go on linux/amd64

The recordings and the parity test run the Go binary built for linux/amd64, and that is the
behaviour the port copies where Go depends on the platform. The one known case: Go's
`int64(f)` for a float at or above 2^63 (such as the JSON number 9223372036854775807) gives
the minimum int64 on amd64 but saturates on arm64, so Go prints that number as
`9.223372036854776e+18` on amd64 and as an integer on arm64. The port prints the amd64 form.

## Known differences

Inputs real Terraform plans do not produce can still diverge: HCL syntax error messages
(hcl-rs words them differently), invalid UTF-8 in a plan (Go replaces each bad byte with
U+FFFD, the port replaces each bad sequence), JSON nested deeper than 128 levels or holding
lone `\u` surrogates (`serde_json` refuses them), regex syntax only Go accepts (`\Q…\E`, `[`
literal inside a class), YAML documents libyaml and yaml-rust2 disagree on, and some Go type
error texts for wrongly typed plan fields. The `server`, `serve`, `ui` and `env_uniqueness`
blocks of a policy file are accepted without checking their contents; Go's render would
reject an invalid one.
