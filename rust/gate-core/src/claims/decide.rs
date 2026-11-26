use jiff::Timestamp;

use super::{
    ClaimAcquired, ClaimReleased, ClaimRenewed, ClaimSet, ClaimStackReleased, Command, Event, LEASE,
};

/// The facts a command produces. Pure: the current time arrives in the command.
pub fn decide(_state: &ClaimSet, c: &Command) -> Vec<Event> {
    match c {
        Command::AcquireClaim(cmd) => vec![
            ClaimAcquired {
                pr: cmd.pr,
                stacks: cmd.stacks.clone(),
                expires_at: expiry(cmd.now),
            }
            .into(),
        ],
        Command::RenewClaim(cmd) => vec![
            ClaimRenewed {
                pr: cmd.pr,
                expires_at: expiry(cmd.now),
            }
            .into(),
        ],
        Command::ReleaseClaim(cmd) => vec![ClaimReleased { pr: cmd.pr }.into()],
        Command::ReleaseClaimStack(cmd) => vec![
            ClaimStackReleased {
                pr: cmd.pr,
                stack: cmd.stack.clone(),
            }
            .into(),
        ],
    }
}

/// `now` plus the lease, saturating at the end of time so decide stays total.
fn expiry(now: Timestamp) -> Timestamp {
    now.checked_add(LEASE).unwrap_or(Timestamp::MAX)
}

#[cfg(test)]
pub(super) mod tests;
