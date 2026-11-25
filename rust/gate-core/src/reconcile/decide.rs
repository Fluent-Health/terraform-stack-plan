use std::collections::{BTreeMap, HashMap, HashSet};

use crate::approval::GrantState;
use crate::events::GateTarget;

use super::decide_inbound::decide_inbound_build;
use super::decide_run::{decide_run_requested, decide_run_start_result, run_completion_events};
use super::{
    BlockReason, ChangeSet, ClaimReleased, Classified, Event, GateBlocked, GatePassed,
    GateReleased, GateSatisfied, GateState, GateTargetRequested, GrantCleared, GrantObserved,
    Lease, ObservedGrant, PRClosedRecorded, RunnerFinalize, Signal, Target, TargetRevoked,
};

/// The past-tense facts that follow from applying `signal` to `state`. All
/// business logic lives here; [`evolve`](super::evolve) only folds.
pub fn decide(state: &ChangeSet, signal: &Signal) -> Vec<Event> {
    match signal {
        Signal::RunnerFinalize(sig) => {
            let mut evs = run_completion_events(state, sig.apply_context);
            evs.extend(decide_finalize(state, sig));
            evs
        }
        Signal::GrantsObserved(sig) => decide_observe(state, &sig.grants, false),
        Signal::GateTick(sig) => decide_observe(state, &sig.grants, true),
        Signal::RunRequested(sig) => decide_run_requested(state, sig),
        Signal::RunStartResult(sig) => decide_run_start_result(state, sig),
        Signal::InboundBuild(sig) => decide_inbound_build(state, sig),
        Signal::ApplySucceeded(_) => {
            if state.gate == GateState::NotClassified {
                return vec![];
            }
            let mut evs: Vec<Event> = vec![
                ClaimReleased {
                    pr: state.pr,
                    environment: state.environment.clone(),
                }
                .into(),
            ];
            evs.extend(revokes(state));
            evs.push(GateReleased {}.into());
            evs
        }
        Signal::PRClosed(_) => {
            let mut evs = revokes(state);
            if evs.is_empty() {
                return vec![];
            }
            evs.push(PRClosedRecorded {}.into());
            evs.push(GateBlocked::new(BlockReason::Revoked).into());
            evs
        }
    }
}

/// A `TargetRevoked` for every target of the gate that has a grant.
fn revokes(state: &ChangeSet) -> Vec<Event> {
    state
        .gate
        .targets()
        .unwrap_or_default()
        .iter()
        .filter(|t| !t.grant_name.is_empty())
        .map(|t| {
            TargetRevoked {
                class: t.class.clone(),
                target: t.target.clone(),
                pr: state.pr,
                env: state.environment.clone(),
            }
            .into()
        })
        .collect()
}

fn key(class: &str, target: &str) -> String {
    format!("{class}|{target}")
}

/// The gate events of a runner finalize:
/// - clean (the effective gate set is empty): `GatePassed`;
/// - gated: `Classified` with the effective set, a `TargetRevoked` for each dropped
///   target (plan finalize only), then a `GateTargetRequested` for the first
///   target without a carried-forward live grant, or `GateSatisfied` when every
///   target carries forward an active grant.
fn decide_finalize(state: &ChangeSet, f: &RunnerFinalize) -> Vec<Event> {
    let prior = prior_targets(&state.gate);

    // Plan finalizes replace the gate set; apply finalizes union it with the prior.
    let gates = if f.apply_context && !prior.is_empty() {
        union_prior_targets(&f.gates, &prior)
    } else {
        f.gates.clone()
    };

    if gates.is_empty() {
        return vec![GatePassed {}.into()];
    }
    let mut evs: Vec<Event> = vec![
        Classified {
            gates: gates.clone(),
        }
        .into(),
    ];

    // Prune (revoke) dropped targets: plan finalize only.
    if !f.apply_context {
        let want: HashSet<String> = gates.iter().map(|g| key(&g.class, &g.target)).collect();
        for (k, pt) in &prior {
            if !want.contains(k) && !pt.grant_name.is_empty() {
                evs.push(
                    TargetRevoked {
                        class: pt.class.clone(),
                        target: pt.target.clone(),
                        pr: state.pr,
                        env: state.environment.clone(),
                    }
                    .into(),
                );
            }
        }
    }

    // Request the first target with no carried-forward grant, mirroring evolve's
    // carry-forward of open grants.
    let lease = state.gate.lease();
    let first_unrequested = gates.iter().find(|g| {
        !prior
            .get(&key(&g.class, &g.target))
            .is_some_and(|pt| pt.grant.open())
    });
    if let Some(g) = first_unrequested {
        evs.push(
            GateTargetRequested {
                class: g.class.clone(),
                target: g.target.clone(),
                requester: lease.requester,
            }
            .into(),
        );
        return evs;
    }

    // Every target carried forward. If all of them are active the gate is already
    // satisfied: say so, or a re-finalized plan whose grants are still live from an
    // earlier build would sit in progress forever.
    let all_active = gates.iter().all(|g| {
        prior
            .get(&key(&g.class, &g.target))
            .is_some_and(|pt| pt.grant == GrantState::Active)
    });
    if all_active {
        evs.push(GateSatisfied {}.into());
    }
    evs
}

/// Folds grant observations into facts, then classifies the gate: a
/// `GrantObserved` per matched target (and a `GrantCleared` per dropped target on
/// a full re-list), then one outcome: `GateBlocked`, a `GateTargetRequested` per
/// re-armable target, `GateSatisfied`, or nothing (the settled awaiting-approval
/// case, which react renders from the folded `Pending` state). Slot collisions
/// short-circuit through [`decide_collision`]. No events for an ungated state.
///
/// A full re-list (`GateTick`) is authoritative: a target the backend no longer
/// reports has lost its grant. Partial feedback (`GrantsObserved`) leaves
/// unmentioned targets alone.
fn decide_observe(state: &ChangeSet, obs: &[ObservedGrant], full_relist: bool) -> Vec<Event> {
    let Some(targets) = state.gate.targets() else {
        return vec![];
    };
    let mut lease = state.gate.lease();
    let prev_was_active = is_all_active(targets);

    // A slot collision decides the whole outcome.
    if let Some(o) = obs.iter().find(|o| o.collision.is_some()) {
        return decide_collision(&lease, o);
    }

    // Reduce several observations of one target to the best, so a stale terminal
    // grant from an earlier retry cannot clobber a live one.
    let mut by_key: HashMap<String, &ObservedGrant> = HashMap::new();
    for o in obs {
        let k = key(&o.class, &o.target);
        match by_key.get(&k) {
            Some(cur) if !fold_better(o, cur, &lease) => {}
            _ => {
                by_key.insert(k, o);
            }
        }
    }

    // Emit the fold facts and build the folded target view to classify on.
    let mut evs: Vec<Event> = vec![];
    let mut local = targets.to_vec();
    for t in &mut local {
        let Some(o) = by_key.get(&key(&t.class, &t.target)) else {
            if full_relist {
                evs.push(
                    GrantCleared {
                        class: t.class.clone(),
                        target: t.target.clone(),
                    }
                    .into(),
                );
                t.grant = GrantState::Absent;
            }
            continue;
        };
        evs.push(
            GrantObserved {
                class: o.class.clone(),
                target: o.target.clone(),
                name: o.name.clone(),
                state: o.state,
                requester: o.requester.clone(),
            }
            .into(),
        );
        if !o.name.is_empty() {
            t.grant_name = o.name.clone();
        }
        t.grant = o.state;
        if lease.requester.is_empty() && !o.requester.is_empty() && o.state.open() {
            lease.requester = o.requester.clone();
        }
    }

    // Denied or revoked blocks. Expired does not: a never-approved lapse is re-armed
    // below, and a was-active expiry downgrades.
    if let Some(reason) = first_terminal_block(&local) {
        evs.push(GateBlocked::new(reason).into());
        return evs;
    }

    // Re-arm every target without an approved grant, pinned to the lease.
    let mut requested = false;
    for t in &local {
        if t.grant_name.is_empty() || (t.grant == GrantState::Expired && !prev_was_active) {
            evs.push(
                GateTargetRequested {
                    class: t.class.clone(),
                    target: t.target.clone(),
                    requester: lease.requester.clone(),
                }
                .into(),
            );
            requested = true;
        }
    }
    if requested {
        return evs;
    }

    if is_all_active(&local) {
        evs.push(GateSatisfied {}.into());
        return evs;
    }

    // Was satisfied, and a grant is now gone without a denial: downgrade.
    if prev_was_active {
        evs.push(GateBlocked::new(BlockReason::Expired).into());
        return evs;
    }

    // Awaiting a human: no outcome fact, the gate stays Pending.
    evs
}

/// The slot-collision policy:
/// - the same PR in another environment blocks (never revoke our own grant);
/// - an open foreign PR blocks, and we wait;
/// - an abandoned foreign PR (closed, not merged) is revoked and our request
///   retried.
fn decide_collision(lease: &Lease, o: &ObservedGrant) -> Vec<Event> {
    let Some(c) = &o.collision else {
        return vec![];
    };
    if c.by_self {
        return vec![
            GateBlocked {
                reason: BlockReason::SlotSelf,
                by_pr: c.by_pr,
                by_env: c.by_env.clone(),
            }
            .into(),
        ];
    }
    if !c.by_pr_abandoned {
        return vec![
            GateBlocked {
                reason: BlockReason::SlotForeign,
                by_pr: c.by_pr,
                by_env: c.by_env.clone(),
            }
            .into(),
        ];
    }
    vec![
        TargetRevoked {
            class: o.class.clone(),
            target: o.target.clone(),
            pr: c.by_pr,
            env: c.by_env.clone(),
        }
        .into(),
        GateTargetRequested {
            class: o.class.clone(),
            target: o.target.clone(),
            requester: lease.requester.clone(),
        }
        .into(),
    ]
}

/// Orders grant states when reducing several observations of one target: active,
/// then the open pending states, then any terminal state, then absent.
fn grant_state_rank(s: GrantState) -> u8 {
    match s {
        GrantState::Active => 4,
        GrantState::Activating => 3,
        GrantState::Awaiting => 2,
        GrantState::Absent => 0,
        GrantState::Denied | GrantState::Revoked | GrantState::Expired => 1,
    }
}

/// Whether observation `a` beats `b` for the same target: higher rank wins, then
/// the one matching the pinned lease, then the greater name, so the choice does not
/// depend on the backend's list order.
fn fold_better(a: &ObservedGrant, b: &ObservedGrant, lease: &Lease) -> bool {
    let (ra, rb) = (grant_state_rank(a.state), grant_state_rank(b.state));
    if ra != rb {
        return ra > rb;
    }
    let a_match = !lease.requester.is_empty() && a.requester == lease.requester;
    let b_match = !lease.requester.is_empty() && b.requester == lease.requester;
    if a_match != b_match {
        return a_match;
    }
    a.name > b.name
}

/// Whether there are targets and every one has an active grant.
fn is_all_active(targets: &[Target]) -> bool {
    !targets.is_empty() && targets.iter().all(|t| t.grant == GrantState::Active)
}

/// The block reason of the first denied or revoked target.
fn first_terminal_block(targets: &[Target]) -> Option<BlockReason> {
    targets.iter().find_map(|t| match t.grant {
        GrantState::Denied => Some(BlockReason::Denied),
        GrantState::Revoked => Some(BlockReason::Revoked),
        _ => None,
    })
}

/// The gate's targets keyed by `class|target`, in key order.
pub(super) fn prior_targets(g: &GateState) -> BTreeMap<String, Target> {
    g.targets()
        .unwrap_or_default()
        .iter()
        .map(|t| (key(&t.class, &t.target), t.clone()))
        .collect()
}

/// The leased requester identity of the gate, empty if none.
pub fn requester(g: &GateState) -> String {
    g.lease().requester
}

/// The finalize gates plus every prior target they do not name, so an apply
/// finalize can only add to the plan's gate. Prior-only targets follow in key
/// order.
fn union_prior_targets(gates: &[GateTarget], prior: &BTreeMap<String, Target>) -> Vec<GateTarget> {
    let seen: HashSet<String> = gates.iter().map(|g| key(&g.class, &g.target)).collect();
    let mut out = gates.to_vec();
    out.extend(
        prior
            .iter()
            .filter(|(k, _)| !seen.contains(*k))
            .map(|(_, pt)| GateTarget {
                class: pt.class.clone(),
                target: pt.target.clone(),
            }),
    );
    out
}

#[cfg(test)]
pub(super) mod tests;
