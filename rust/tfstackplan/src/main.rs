//! The `tfstackplan` binary. All of its logic is in the library crate, so it stays this small.

#![forbid(unsafe_code)]

use std::process::ExitCode;

fn main() -> ExitCode {
    tfstackplan::main()
}
