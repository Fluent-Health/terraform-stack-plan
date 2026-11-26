use super::{Claim, ClaimSet, Event};

/// Folds one event into the ledger. Total, pure and deterministic.
pub fn evolve(mut s: ClaimSet, e: &Event) -> ClaimSet {
    match e {
        Event::ClaimAcquired(ev) => {
            for stack in &ev.stacks {
                s.insert(
                    stack.clone(),
                    Claim {
                        pr: ev.pr,
                        expires_at: ev.expires_at,
                    },
                );
            }
        }
        Event::ClaimRenewed(ev) => {
            for c in s.values_mut().filter(|c| c.pr == ev.pr) {
                c.expires_at = ev.expires_at;
            }
        }
        Event::ClaimReleased(ev) => s.retain(|_, c| c.pr != ev.pr),
        Event::ClaimStackReleased(ev) => {
            if s.get(&ev.stack).is_some_and(|c| c.pr == ev.pr) {
                s.remove(&ev.stack);
            }
        }
    }
    s
}

#[cfg(test)]
mod tests;
