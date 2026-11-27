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


## The CLI faces: crates and seams

`state`, `claims`, `catalog`, `uniqueness`, `whoami`, `admin` and `run wrap` are ported
together because none needs the renderer. Their logic is in `cli-core` (package
`tfstackplan-cli`): it reads no files, runs no processes, reads no clock and is not async,
so each Go unit test ports to a plain Rust test. The binary's face modules are the shell:
they parse flags, walk directories, run terraform and terramate with `std::process`, call
the clients and print. `state apply`'s executor talks to terraform through a `Runner`
trait, so the Go tests that fake terraform port unchanged. `run wrap`, `run claims` and
`run whoami` are routed to Rust through a second table, `PORTED_RUN`; the rest of `run`
stays in Go.

## Byte-for-byte output needs Go's formatting rules

Usage text, error messages, HCL shims and JSON are compared byte for byte with Go's, so
`cli-core` carries small copies of the Go behaviour they depend on: the `flag` package
(parsing rules, messages, `PrintDefaults` layout), `strconv.Quote` (`%q`), the `%v` form of
a float64, `encoding/json`'s HTML-safe escapes, float format and `MarshalIndent` layout,
`filepath.Match`, and `url.QueryEscape`/`PathEscape`. Go regexps compile on the `regex`
crate after rewriting `\d`, `\w`, `\s` and `\b` to their ASCII forms, because Go's are
ASCII-only. Where Go's output depends on random map iteration (component order in
`catalog`, `state move` printing several stacks, environments discovered from terramate or
the config in `claims list`), Rust sorts; replay cases avoid those paths.

## HCL and YAML parsers

HCL is parsed with `hcl-rs` (built on `hcl-edit`), evaluated with no variables in scope like
Go's `expr.Value(nil)`. A small decoder in `cli-core` rejects unknown arguments and blocks
the way `gohcl` does. Go's HCL diagnostics carry a file position and Rust's do not, so a
malformed file fails in the same cases with a different message; replay cases only cover
the errors tfstackplan itself words. The shims and xmove manifests are built as strings,
as Go builds them. YAML (uniqueness manifests) is parsed with `yaml-rust2` and converted to
the values Go's yaml.v3 yields for `map[string]any`; a YAML syntax error is worded by
`yaml-rust2`. Merge keys (`<<`) in uniqueness manifests are kept as ordinary keys where
yaml.v3 merges them; the `run` port branch closes that gap. Until it merges, no release may
be cut from main: `uniqueness` defaults to Rust and would misread a manifest that uses `<<`.

## HTTP clients are blocking

The CLI runs one request at a time, so `clients` uses reqwest's blocking client rather than
an async runtime, each with a connect timeout and a total timeout (Go's lock client had
none). TLS is rustls with aws-lc-rs only: the front door installs that provider before a
ported face runs, so rustls never has to choose between providers at runtime.
Service-account keys are signed with aws-lc-rs, which rustls already links. Google
credentials follow Go's lookup order (`GOOGLE_APPLICATION_CREDENTIALS`, the gcloud
well-known file, the metadata server when on Google Cloud) and Go's ID-token strategies,
including the Cloud Build fallback to `generateIdToken`. `external_account` credentials are
not supported. Tokens are reused until shortly before they expire, with each Go library's
margin: 10 s for access tokens, 225 s for ID tokens (a service-account key's ID token takes
its expiry from its own `exp` claim, as the reply has no `expires_in`) and 5 min for the
metadata server's. Metadata server calls use their own client with no proxy, as Go's
metadata client does, so `HTTP_PROXY` on a runner cannot send them elsewhere. Error text
matches Go's where the CLI prints it (ADC discovery, oauth2
token errors, server responses); transport errors carry reqwest's wording.

## Replay cases run hermetically, with fake servers

Faces that call the control plane, Google or GCS are recorded against a fake server, not
real ones: the recorder and the replay both start it per run, from routes in the case's
`[harness]` table, and point both binaries at it through `TFSTACKPLAN_SERVER`,
`TFSTACKPLAN_GCS_URL`, an ADC file whose `token_uri` is on the fake server, or
`GCE_METADATA_HOST`. The server is a few dozen lines of std networking in `casekit`, shared
by the recorder and the replay so both answer identically; wiremock stays in the clients'
unit tests, where requests are asserted. Every case runs with an empty `HOME` and the
credential and server variables cleared, so a developer's own credentials never reach a
recording. Cases that need terraform or terramate say so and are skipped, with a message,
when the program is missing.

## `run wrap` uses portable-pty

`run wrap --tty` needs a pty that becomes the command's controlling terminal. Doing that
with `std::process` needs `pre_exec`, which is unsafe, and every crate here forbids unsafe
code, so the pty comes from `portable-pty`. Like Go's `creack/pty` it starts the command in
a new session with the pty as its controlling terminal and nothing written to its input.
Two differences: portable-pty sets `SHELL` in the command's environment and resets the
command's signal dispositions to the default. Neither wrapper installs signal handlers, so
Ctrl-C and the exit code behave as in Go; a command killed by a signal exits the wrapper
with 255, as Go's `os.Exit(-1)` does.

## `state apply` runs terraform the way terraform-exec does

The Go code drives terraform through terraform-exec v0.25.1. The Rust runner builds the same
argv for each call (`init`, `state pull`, `state push`, `show -json`, `state mv`) and the
same environment (`TF_IN_AUTOMATION=1`, the `TF_LOG*` variables cleared, the user agent
suffix, `TF_WORKSPACE` removed), so terraform behaves the same under either binary. It skips
terraform-exec's version probe.

terraform-exec also starts terraform in its own process group with a parent-death signal
(SIGKILL), so terraform dies with its parent. The parent-death signal needs an unsafe
`pre_exec`, which no crate here allows, and a separate process group without it would let
terraform outlive a cancelled CI job and keep pushing state with nobody watching. So the
Rust runner leaves terraform in our process group: the signal that stops the job reaches
both. The replay runs `state apply` against real terraform with a local backend and
the builtin `terraform_data` resource, so no provider is downloaded. `--lock` is covered by
the clients' wiremock tests only: terraform itself cannot talk to the fake GCS.
