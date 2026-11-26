// Ported case by case from internal/reconcile/decide_test.go.
use std::collections::BTreeMap;

use crate::approval::GrantState;
use crate::events::GateTarget;
use crate::reconcile::*;

pub(crate) fn gt(class: &str, target: &str) -> GateTarget {
    GateTarget {
        class: class.into(),
        target: target.into(),
    }
}

pub(crate) fn tgt(class: &str, target: &str, grant_name: &str, grant: GrantState) -> Target {
    Target {
        class: class.into(),
        target: target.into(),
        grant_name: grant_name.into(),
        grant,
    }
}

pub(crate) fn lease(requester: &str) -> Lease {
    Lease {
        requester: requester.into(),
    }
}

pub(crate) fn pending(targets: Vec<Target>, requester: &str) -> GateState {
    GateState::Pending(Pending {
        targets,
        lease: lease(requester),
    })
}

pub(crate) fn satisfied(targets: Vec<Target>, requester: &str) -> GateState {
    GateState::Satisfied(Satisfied {
        targets,
        lease: lease(requester),
    })
}

fn cs(pr: i64, env: &str, gate: GateState) -> ChangeSet {
    ChangeSet {
        pr,
        environment: env.into(),
        gate,
        ..ChangeSet::default()
    }
}

fn finalize(gates: Vec<GateTarget>) -> Signal {
    RunnerFinalize {
        gates,
        apply_context: false,
    }
    .into()
}

fn observed(class: &str, target: &str, name: &str, state: GrantState) -> ObservedGrant {
    ObservedGrant {
        class: class.into(),
        target: target.into(),
        name: name.into(),
        state,
        ..ObservedGrant::default()
    }
}

fn grants_observed(grants: Vec<ObservedGrant>) -> Signal {
    GrantsObserved { grants }.into()
}

fn gate_tick(grants: Vec<ObservedGrant>) -> Signal {
    GateTick { grants }.into()
}

// --- RunnerFinalize ---

#[test]
fn decide_finalize_clean_gate_passed() {
    let got = decide(&ChangeSet::default(), &finalize(vec![]));
    assert_eq!(got.last(), Some(&GatePassed {}.into()), "{got:?}");
}

#[test]
fn decide_finalize_gated_requests_first() {
    let got = decide(
        &cs(7, "nonprod", GateState::NotClassified),
        &finalize(vec![gt("c", "t")]),
    );
    assert!(!events_of!(got, Classified).is_empty(), "{got:?}");
    assert!(!events_of!(got, GateTargetRequested).is_empty(), "{got:?}");
}

#[test]
fn decide_finalize_gated_prunes_dropped() {
    // Prior p2 (has a grant name) is not in the new gates: it is revoked.
    let state = cs(
        7,
        "staging",
        pending(
            vec![
                tgt("iam", "p1", "g1", GrantState::Active),
                tgt("iam", "p2", "g2", GrantState::Denied),
            ],
            "",
        ),
    );
    let got = decide(&state, &finalize(vec![gt("iam", "p1")]));
    let revoked = events_of!(got, TargetRevoked);
    assert_eq!(revoked.len(), 1, "{revoked:?}");
    assert_eq!(revoked[0].target, "p2");
}

#[test]
fn decide_finalize_apply_context_no_prune() {
    let state = cs(
        7,
        "prod",
        satisfied(
            vec![tgt("iam", "proj-a", "g1", GrantState::Active)],
            "tf-applier-0@x",
        ),
    );
    let got = decide(
        &state,
        &RunnerFinalize {
            gates: vec![],
            apply_context: true,
        }
        .into(),
    );
    assert!(events_of!(got, TargetRevoked).is_empty(), "{got:?}");
}

#[test]
fn decide_finalize_requester_threaded() {
    let state = cs(
        7,
        "staging",
        pending(vec![tgt("iam", "p1", "", GrantState::Absent)], "sa1"),
    );
    let got = decide(&state, &finalize(vec![gt("iam", "p1")]));
    let req = events_of!(got, GateTargetRequested);
    assert_eq!(
        req.first().map(|r| r.requester.as_str()),
        Some("sa1"),
        "{got:?}"
    );
}

#[test]
fn decide_finalize_carry_forward_skips_request() {
    // p1 has a live grant and carries forward; p2 is fresh and is requested.
    let state = cs(
        7,
        "staging",
        pending(vec![tgt("iam", "p1", "g-p1", GrantState::Active)], "sa1"),
    );
    let got = decide(&state, &finalize(vec![gt("iam", "p1"), gt("iam", "p2")]));
    let reqs = events_of!(got, GateTargetRequested);
    assert!(reqs.iter().all(|r| r.target != "p1"), "{got:?}");
    assert!(reqs.iter().any(|r| r.target == "p2"), "{got:?}");
}

#[test]
fn decide_finalize_carry_forward_all_active_satisfies() {
    // A new build re-finalizes while the earlier build's grants are all active:
    // nothing is requested, and the gate must be satisfied explicitly.
    let state = cs(
        7,
        "prod",
        satisfied(
            vec![
                tgt("iam", "p1", "g-p1", GrantState::Active),
                tgt("iam", "p2", "g-p2", GrantState::Active),
            ],
            "tf-applier-0@x",
        ),
    );
    let got = decide(&state, &finalize(vec![gt("iam", "p1"), gt("iam", "p2")]));
    assert!(events_of!(got, GateTargetRequested).is_empty(), "{got:?}");
    assert!(last_of!(got, GateSatisfied).is_some(), "{got:?}");
}

#[test]
fn decide_finalize_carry_forward_all_awaiting_no_satisfy() {
    let state = cs(
        7,
        "staging",
        pending(vec![tgt("iam", "p1", "g-p1", GrantState::Awaiting)], "sa1"),
    );
    let got = decide(&state, &finalize(vec![gt("iam", "p1")]));
    assert!(last_of!(got, GateSatisfied).is_none(), "{got:?}");
    assert!(events_of!(got, GateTargetRequested).is_empty(), "{got:?}");
}

#[test]
fn decide_apply_succeeded_not_classified_no_events() {
    let got = decide(
        &cs(0, "", GateState::NotClassified),
        &ApplySucceeded {}.into(),
    );
    assert!(got.is_empty(), "{got:?}");
}

#[test]
fn decide_apply_succeeded_releases_claim_and_revokes_grants() {
    let state = cs(
        7,
        "nonprod",
        satisfied(vec![tgt("c", "t", "g1", GrantState::Active)], ""),
    );
    let got = decide(&state, &ApplySucceeded {}.into());
    let want: Vec<Event> = vec![
        ClaimReleased {
            pr: 7,
            environment: "nonprod".into(),
        }
        .into(),
        TargetRevoked {
            class: "c".into(),
            target: "t".into(),
            pr: 7,
            env: "nonprod".into(),
        }
        .into(),
        GateReleased {}.into(),
    ];
    assert_eq!(got, want);
}

#[test]
fn decide_pr_closed_no_targets_no_events() {
    let got = decide(&cs(0, "", GateState::Clean), &PRClosed {}.into());
    assert!(got.is_empty(), "{got:?}");
}

// --- GrantsObserved / GateTick (observe + collision) ---

#[test]
fn decide_observe_non_gated_no_events() {
    // Go also passes a nil gate; the Rust default is NotClassified.
    for g in [
        GateState::NotClassified,
        GateState::Clean,
        GateState::default(),
    ] {
        let got = decide(&cs(0, "", g.clone()), &grants_observed(vec![]));
        assert!(got.is_empty(), "{g:?}: {got:?}");
    }
}

#[test]
fn decide_observe_all_active_satisfied() {
    let state = cs(
        0,
        "",
        pending(vec![tgt("c", "t", "g1", GrantState::Active)], ""),
    );
    let got = decide(
        &state,
        &grants_observed(vec![observed("c", "t", "g1", GrantState::Active)]),
    );
    assert!(last_of!(got, GateSatisfied).is_some(), "{got:?}");
    let obs = events_of!(got, GrantObserved);
    assert_eq!(obs.len(), 1, "{got:?}");
    assert_eq!(obs[0].state, GrantState::Active);
}

#[test]
fn decide_observe_denied_blocks() {
    let state = cs(
        0,
        "",
        pending(
            vec![
                tgt("c", "t", "", GrantState::Absent),
                tgt("c", "u", "", GrantState::Absent),
            ],
            "",
        ),
    );
    let got = decide(
        &state,
        &grants_observed(vec![observed("c", "t", "g1", GrantState::Denied)]),
    );
    assert_eq!(
        last_of!(got, GateBlocked).map(|b| b.reason),
        Some(BlockReason::Denied),
        "{got:?}"
    );
}

#[test]
fn decide_observe_revoked_blocks() {
    let state = cs(
        0,
        "",
        pending(vec![tgt("c", "t", "", GrantState::Absent)], ""),
    );
    let got = decide(
        &state,
        &grants_observed(vec![observed("c", "t", "g1", GrantState::Revoked)]),
    );
    assert_eq!(
        last_of!(got, GateBlocked).map(|b| b.reason),
        Some(BlockReason::Revoked),
        "{got:?}"
    );
}

#[test]
fn decide_observe_ungranted_re_arms_all() {
    let state = cs(
        0,
        "",
        pending(
            vec![
                tgt("c", "t", "", GrantState::Absent),
                tgt("c", "u", "", GrantState::Absent),
            ],
            "sa3",
        ),
    );
    let got = decide(
        &state,
        &grants_observed(vec![ObservedGrant {
            requester: "sa3".into(),
            ..observed("c", "t", "g1", GrantState::Awaiting)
        }]),
    );
    // t has a grant now; u is still ungranted: one re-arm for u, pinned.
    let reqs = events_of!(got, GateTargetRequested);
    assert_eq!(
        reqs,
        vec![GateTargetRequested {
            class: "c".into(),
            target: "u".into(),
            requester: "sa3".into()
        }],
        "{got:?}"
    );
}

#[test]
fn decide_observe_lapsed_expired_re_arms() {
    let state = cs(
        0,
        "",
        pending(vec![tgt("c", "t", "g1", GrantState::Awaiting)], "sa3"),
    );
    let got = decide(
        &state,
        &gate_tick(vec![observed("c", "t", "g1", GrantState::Expired)]),
    );
    let reqs = events_of!(got, GateTargetRequested);
    assert_eq!(reqs.len(), 1, "{got:?}");
    assert_eq!(reqs[0].target, "t");
    assert_eq!(reqs[0].requester, "sa3");
}

#[test]
fn decide_tick_drops_target_clears_and_downgrades() {
    let state = cs(
        0,
        "",
        satisfied(vec![tgt("c", "t", "g1", GrantState::Active)], "sa3"),
    );
    let got = decide(&state, &gate_tick(vec![])); // the full re-list omits t
    assert!(last_of!(got, GrantCleared).is_some(), "{got:?}");
    assert_eq!(
        last_of!(got, GateBlocked).map(|b| b.reason),
        Some(BlockReason::Expired),
        "{got:?}"
    );
}

#[test]
fn decide_observe_partial_leaves_unmentioned_untouched() {
    let state = cs(
        0,
        "",
        satisfied(vec![tgt("c", "t", "g1", GrantState::Active)], "sa3"),
    );
    let got = decide(&state, &grants_observed(vec![])); // partial: mentions nothing
    assert!(events_of!(got, GrantCleared).is_empty(), "{got:?}");
    assert!(last_of!(got, GateSatisfied).is_some(), "{got:?}");
}

#[test]
fn decide_observe_settled_pending_emits_no_outcome() {
    let state = cs(
        0,
        "",
        pending(vec![tgt("c", "t", "g1", GrantState::Awaiting)], "sa0"),
    );
    let got = decide(
        &state,
        &gate_tick(vec![observed("c", "t", "g1", GrantState::Awaiting)]),
    );
    assert!(last_of!(got, GateSatisfied).is_none(), "{got:?}");
    assert!(last_of!(got, GateBlocked).is_none(), "{got:?}");
    assert!(events_of!(got, GateTargetRequested).is_empty(), "{got:?}");
    assert_eq!(events_of!(got, GrantObserved).len(), 1, "{got:?}");
}

fn collision(c: Collision) -> Signal {
    grants_observed(vec![ObservedGrant {
        collision: Some(c),
        ..observed("c", "t", "", GrantState::Absent)
    }])
}

#[test]
fn decide_collision_self_blocks() {
    let state = cs(
        7,
        "",
        pending(vec![tgt("c", "t", "", GrantState::Absent)], ""),
    );
    let got = decide(
        &state,
        &collision(Collision {
            by_pr: 7,
            by_env: "prod".into(),
            by_self: true,
            by_pr_abandoned: false,
        }),
    );
    let gb = last_of!(got, GateBlocked).expect("GateBlocked");
    assert_eq!(gb.reason, BlockReason::SlotSelf);
    assert_eq!(gb.by_env, "prod");
}

#[test]
fn decide_collision_foreign_open_blocks() {
    let state = cs(
        8,
        "",
        pending(vec![tgt("c", "t", "", GrantState::Absent)], ""),
    );
    let got = decide(
        &state,
        &collision(Collision {
            by_pr: 5,
            by_env: "staging".into(),
            by_self: false,
            by_pr_abandoned: false,
        }),
    );
    let gb = last_of!(got, GateBlocked).expect("GateBlocked");
    assert_eq!(gb.reason, BlockReason::SlotForeign);
    assert_eq!(gb.by_pr, 5);
}

#[test]
fn decide_collision_abandoned_revokes_foreign_then_requests() {
    let state = cs(
        8,
        "staging",
        pending(vec![tgt("c", "t", "", GrantState::Absent)], ""),
    );
    let got = decide(
        &state,
        &collision(Collision {
            by_pr: 7,
            by_env: "staging".into(),
            by_self: false,
            by_pr_abandoned: true,
        }),
    );
    let want: Vec<Event> = vec![
        TargetRevoked {
            class: "c".into(),
            target: "t".into(),
            pr: 7,
            env: "staging".into(),
        }
        .into(),
        GateTargetRequested {
            class: "c".into(),
            target: "t".into(),
            requester: String::new(),
        }
        .into(),
    ];
    assert_eq!(got, want);
}

#[test]
fn decide_pr_closed_revokes_and_blocks() {
    let state = cs(
        7,
        "nonprod",
        pending(vec![tgt("c", "t", "g1", GrantState::Active)], ""),
    );
    let got = decide(&state, &PRClosed {}.into());
    let want: Vec<Event> = vec![
        TargetRevoked {
            class: "c".into(),
            target: "t".into(),
            pr: 7,
            env: "nonprod".into(),
        }
        .into(),
        PRClosedRecorded {}.into(),
        GateBlocked::new(BlockReason::Revoked).into(),
    ];
    assert_eq!(got, want);
}

mod decide_inbound_build {
    use super::*;

    fn base() -> ChangeSet {
        cs(7, "nonprod", GateState::NotClassified)
    }

    fn dead_run(phase: RunPhase, build_ref: &str) -> BTreeMap<String, Run> {
        BTreeMap::from([(
            RUN_KIND_PLAN.to_string(),
            Run {
                execution_id: "run-7-nonprod-plan-abc123abc123-a1".into(),
                kind: RUN_KIND_PLAN.into(),
                sha: "abc123abc123def".into(),
                branch: "feat/x".into(),
                attempt: 1,
                build_ref: build_ref.into(),
                phase,
            },
        )])
    }

    fn inbound(kind: &str, sha: &str, build_ref: &str) -> Signal {
        InboundBuild {
            kind: kind.into(),
            sha: sha.into(),
            build_ref: build_ref.into(),
        }
        .into()
    }

    #[test]
    fn new_build_for_a_start_failed_run_supersedes_and_adopts() {
        let mut state = base();
        state.runs = dead_run(RunPhase::StartFailed, "build-old");
        let evs = decide(
            &state,
            &inbound(RUN_KIND_PLAN, "abc123abc123def", "build-new"),
        );
        let want_new = "run-7-nonprod-plan-abc123abc123-a2";
        let want: Vec<Event> = vec![
            RunSuperseded {
                kind: RUN_KIND_PLAN.into(),
                old_execution_id: "run-7-nonprod-plan-abc123abc123-a1".into(),
                old_build_ref: "build-old".into(),
                new_execution_id: want_new.into(),
                new_sha: "abc123abc123def".into(),
            }
            .into(),
            RunAdopted {
                kind: RUN_KIND_PLAN.into(),
                execution_id: want_new.into(),
                sha: "abc123abc123def".into(),
                branch: "feat/x".into(),
                attempt: 2,
                build_ref: "build-new".into(),
            }
            .into(),
        ];
        assert_eq!(evs, want);
    }

    #[test]
    fn same_build_id_is_idempotent_no_op() {
        let mut state = base();
        state.runs = dead_run(RunPhase::StartFailed, "build-old");
        assert!(
            decide(
                &state,
                &inbound(RUN_KIND_PLAN, "abc123abc123def", "build-old")
            )
            .is_empty()
        );
    }

    #[test]
    fn live_run_is_left_alone() {
        let mut state = base();
        state.runs = dead_run(RunPhase::Started, "build-old");
        assert!(
            decide(
                &state,
                &inbound(RUN_KIND_PLAN, "abc123abc123def", "build-new")
            )
            .is_empty()
        );
    }

    #[test]
    fn different_sha_is_ignored() {
        let mut state = base();
        state.runs = dead_run(RunPhase::StartFailed, "build-old");
        assert!(
            decide(
                &state,
                &inbound(RUN_KIND_PLAN, "othersha000000", "build-new")
            )
            .is_empty()
        );
    }

    #[test]
    fn no_serve_run_is_ignored() {
        assert!(
            decide(
                &base(),
                &inbound(RUN_KIND_PLAN, "abc123abc123def", "build-new")
            )
            .is_empty()
        );
    }

    #[test]
    fn unknown_kind_is_ignored() {
        let mut state = base();
        state.runs = dead_run(RunPhase::StartFailed, "build-old");
        assert!(decide(&state, &inbound("bogus", "abc123abc123def", "build-new")).is_empty());
    }

    #[test]
    fn empty_build_ref_is_ignored() {
        let mut state = base();
        state.runs = dead_run(RunPhase::StartFailed, "build-old");
        assert!(decide(&state, &inbound(RUN_KIND_PLAN, "abc123abc123def", "")).is_empty());
    }
}

// --- a backend state outside the normalised set (Go passes it through) ---

#[test]
fn decide_observe_unknown_state_is_closed_and_does_not_block() {
    let state = cs(
        7,
        "",
        pending(vec![tgt("c", "t", "", GrantState::Absent)], "sa0"),
    );
    let unknown = GrantState::Other("ACTIVATION_FAILED".into());
    let got = decide(
        &state,
        &grants_observed(vec![observed("c", "t", "g1", unknown.clone())]),
    );
    // Folded as observed, with no block, re-arm or satisfaction: the gate waits.
    let obs = events_of!(got, GrantObserved);
    assert_eq!(obs.len(), 1, "{got:?}");
    assert_eq!(obs[0].state, unknown);
    assert!(last_of!(got, GateBlocked).is_none(), "{got:?}");
    assert!(events_of!(got, GateTargetRequested).is_empty(), "{got:?}");
    assert!(last_of!(got, GateSatisfied).is_none(), "{got:?}");
}

#[test]
fn decide_observe_prefers_an_open_grant_over_an_unknown_one() {
    let state = cs(
        7,
        "",
        pending(vec![tgt("c", "t", "", GrantState::Absent)], ""),
    );
    let got = decide(
        &state,
        &gate_tick(vec![
            observed("c", "t", "g9", GrantState::Other("WITHDRAWN".into())),
            observed("c", "t", "g1", GrantState::Awaiting),
        ]),
    );
    let obs = events_of!(got, GrantObserved);
    assert_eq!(obs.len(), 1, "{got:?}");
    assert_eq!(
        (obs[0].name.as_str(), &obs[0].state),
        ("g1", &GrantState::Awaiting)
    );
}

#[test]
fn decide_observe_gate_without_targets_no_events() {
    // Go's nil target slice: even a collision decides nothing.
    let state = cs(7, "", pending(vec![], ""));
    let got = decide(
        &state,
        &collision(Collision {
            by_pr: 5,
            by_env: "prod".into(),
            by_self: false,
            by_pr_abandoned: false,
        }),
    );
    assert!(got.is_empty(), "{got:?}");
}
