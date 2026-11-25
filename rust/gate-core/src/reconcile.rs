//! The gate and run-start aggregate for one PR in one environment (Go:
//! `internal/reconcile`), stream `exec:<pr>:<env>`.
//!
//! [`decide`] turns a [`Signal`] into past-tense [`Event`]s, [`evolve`] folds an
//! event into the [`ChangeSet`], and [`react`] derives the [`Action`]s the shell
//! runs. All business logic is in `decide`; `evolve` only folds, and is total.
//!
//! ```
//! use tfstackplan_gate::events::GateTarget;
//! use tfstackplan_gate::reconcile::*;
//!
//! let prior = ChangeSet { pr: 7, environment: "staging".into(), ..ChangeSet::default() };
//! let signal = RunnerFinalize {
//!     gates: vec![GateTarget { class: "iam".into(), target: "p1".into() }],
//!     apply_context: false,
//! };
//! let events = decide(&prior, &signal.into());
//! let state = events.iter().fold(prior, evolve);
//! let actions = react(&state, &events);
//!
//! assert!(matches!(state.gate, GateState::Pending(_)));
//! assert!(matches!(actions[0], Action::RequestGrant(_)));
//! ```

// Test helpers: every event of one variant in a batch, and the last one.
#[cfg(test)]
macro_rules! events_of {
    ($evs:expr, $variant:ident) => {
        $evs.iter()
            .filter_map(|e| match e {
                Event::$variant(v) => Some(v.clone()),
                _ => None,
            })
            .collect::<Vec<_>>()
    };
}
#[cfg(test)]
macro_rules! last_of {
    ($evs:expr, $variant:ident) => {
        events_of!($evs, $variant).pop()
    };
}

mod action;
mod codec;
mod decide;
mod decide_inbound;
mod decide_run;
mod event;
mod evolve;
mod react;
mod signal;
mod state;

pub use action::*;
pub use codec::{Gate, marshal_event, marshal_snapshot, unmarshal_event, unmarshal_snapshot};
pub use decide::{decide, requester};
pub use decide_run::{RUN_EXECUTION_ID_PREFIX, is_run_execution_id};
pub use event::*;
pub use evolve::evolve;
pub use react::react;
pub use signal::*;
pub use state::*;

/// Implements `From<Variant> for Enum` for enums whose variants each wrap a struct
/// of the same name, so `GateSatisfied {}.into()` builds an `Event`.
macro_rules! wrap_variants {
    ($enum:ident: $($variant:ident),+ $(,)?) => {
        $(
            impl From<$variant> for $enum {
                fn from(v: $variant) -> Self {
                    $enum::$variant(v)
                }
            }
        )+
    };
}
pub(crate) use wrap_variants;
