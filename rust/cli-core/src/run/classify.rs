//! The classify pass's cross-state move check (Go's `validateXMoveManifest`):
//! every pending xmove manifest is checked against the plans the pass is about to
//! classify, so a stale or impossible move fails the pass instead of a relocation
//! classifying as a real destroy and create.

use crate::gofmt::quote;
use crate::statemove::manifest::{FoundXMove, resolve_source_stack};
use crate::statemove::plan::{change_addresses, parse_plan};
use crate::statemove::{
    AddressSet, Severity, data_source_orphans, dest_providers, is_spent, prior_state_addrs,
    prior_state_data_sources, validate_move_plan,
};

/// What the check needs from the filesystem.
pub struct Inputs<'a> {
    /// A stack's `tfplan.json` in the gathered plans dir, if there is one.
    pub plan: &'a dyn Fn(&str) -> Option<Vec<u8>>,
    /// Whether a stack directory exists under the project root.
    pub exists: &'a dyn Fn(&str) -> bool,
    /// The `.tf` file contents of a stack directory under the project root.
    pub tf_files: &'a dyn Fn(&str) -> Vec<String>,
}

/// The diagnostics Go prints to stderr, and whether any was an error.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Checked {
    /// Lines for stderr, each ending in a newline.
    pub stderr: String,
    /// At least one error: the pass fails with `xmove manifest validation failed`.
    pub failed: bool,
}

/// Checks every manifest.
pub fn validate_xmoves(xmoves: &[FoundXMove], io: &Inputs<'_>) -> Checked {
    let mut out = Checked::default();
    for fx in xmoves {
        let source = resolve_source_stack(&fx.dest_stack, &fx.xmove.source_stack, io.exists);
        let dst_bytes = (io.plan)(&fx.dest_stack);
        let dst_prior: AddressSet = dst_bytes
            .as_deref()
            .and_then(prior_state_addrs)
            .unwrap_or_default();

        let Some(src_bytes) = (io.plan)(&source) else {
            if is_spent(&fx.xmove.pairs, &dst_prior) {
                out.stderr.push_str(&format!(
                    "\u{2139}\u{fe0f}  xmove {}: xmove/spent — all declared moves already applied; run 'state cleanup --applied' to remove this manifest\n",
                    fx.key
                ));
            } else {
                out.stderr.push_str(&format!(
                    "\u{274c} xmove {}: xmove/source-not-planned — source stack {} is not in the changed set; touch it so Terramate plans it, or remove this manifest if the move is already complete\n",
                    fx.key,
                    quote(&source)
                ));
                out.failed = true;
            }
            continue;
        };
        let Some(prior) = prior_state_addrs(&src_bytes) else {
            out.stderr.push_str(&format!(
                "\u{274c} xmove {}: source stack {} has no prior_state (stack is new — nothing to move)\n",
                fx.key,
                quote(&source)
            ));
            out.failed = true;
            continue;
        };
        let data_sources = prior_state_data_sources(&src_bytes).unwrap_or_default();
        for ds in data_source_orphans(&fx.xmove.pairs, &data_sources) {
            out.stderr.push_str(&format!(
                "\u{26a0}\u{fe0f}  xmove {}: xmove/data-source-orphan — {} falls under the from prefix but data sources cannot be moved; it will remain in the source stack (run 'terraform state rm {ds}' in the source to clean up)\n",
                fx.key,
                quote(&ds)
            ));
        }
        let dst_addrs = dst_bytes
            .as_deref()
            .and_then(|b| parse_plan(b).ok())
            .map(|p| change_addresses(&p))
            .unwrap_or_default();
        let files = (io.tf_files)(&fx.dest_stack);
        let providers = dest_providers(files.iter().map(String::as_str));
        for d in validate_move_plan(&prior, &dst_addrs, &dst_prior, &providers, &fx.xmove, false) {
            if d.severity == Severity::Error {
                out.stderr
                    .push_str(&format!("\u{274c} xmove {}: {}\n", fx.key, d.message));
                out.failed = true;
            } else if d.code == "xmove/spent" {
                out.stderr.push_str(&format!(
                    "\u{2139}\u{fe0f}  xmove {}: xmove/spent — {}\n",
                    fx.key, d.message
                ));
            } else {
                out.stderr.push_str(&format!(
                    "\u{26a0}\u{fe0f}  xmove {}: {}\n",
                    fx.key, d.message
                ));
            }
        }
    }
    out
}

#[cfg(test)]
mod tests;
