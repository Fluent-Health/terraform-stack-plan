//! `catalog`: the component graph of a terramate project, as JSON.

use std::path::Path;
use std::process::ExitCode;

use tfstackplan_cli::catalog;
use tfstackplan_cli::flag::FlagSet;

use super::{err, out, parse};
use crate::proc;

/// Runs `catalog`.
pub fn run(args: &[String]) -> ExitCode {
    let mut fs = FlagSet::new("catalog build");
    fs.string("dir", ".", "terramate project root (required)")
        .string("o", "", "output file path (default: stdout)");
    let p = match parse(&fs, args) {
        Ok(p) => p,
        Err(c) => return c,
    };
    let dir = Path::new(p.str("dir"));
    let stacks = match proc::terramate(dir, &["list"]) {
        Ok(stdout) => proc::lines(&stdout),
        Err(e) => {
            err(&format!("catalog build: list stacks: {e}\n"));
            return ExitCode::FAILURE;
        }
    };
    let watches = |stack: &str| {
        let stack_dir = dir.join(stack);
        for file in ["stack.tm.hcl", "terramate.tm.hcl", ".tm.hcl"] {
            if let Ok(src) = std::fs::read_to_string(stack_dir.join(file))
                && let Ok(w) = catalog::parse_watches(&src)
                && !w.is_empty()
            {
                return w;
            }
        }
        Vec::new()
    };
    let graph = proc::terramate(dir, &["experimental", "run-graph", "-l", "stack.dir"])
        .map(|stdout| catalog::parse_run_graph(&String::from_utf8_lossy(&stdout)))
        .unwrap_or_default();
    let cat = catalog::build(&stacks, &watches, &graph);
    let data = match tfstackplan_cli::gojson::marshal_indent(&catalog::to_json(&cat), "  ") {
        Ok(d) => d,
        Err(e) => {
            err(&format!("catalog build: marshal: {e}\n"));
            return ExitCode::FAILURE;
        }
    };
    let o = p.str("o");
    if o.is_empty() || o == "-" {
        out(&format!("{data}\n"));
    } else if let Err(e) = std::fs::write(o, data) {
        err(&format!(
            "catalog build: write file: {}\n",
            crate::fsx::os_error("open", Path::new(o), &e)
        ));
        return ExitCode::FAILURE;
    }
    ExitCode::SUCCESS
}
