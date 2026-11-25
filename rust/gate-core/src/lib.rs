//! The pure core behind `tfstackplan serve`, ported from Go.
//!
//! Every gate and execution state change goes through a decider: `decide(state,
//! signal) -> events`, `evolve(state, event) -> state` and `react(state, events) ->
//! actions`. Nothing here does I/O, reads a clock or runs async: time comes in as a
//! value, and the shell (the store crate and, later, the HTTP layer) persists the
//! events and runs the actions.
//!
//! | Go package | module |
//! |---|---|
//! | `internal/domain` | [`domain`] |
//! | `internal/events` | [`events`] |
//! | `internal/approval` (types only) | [`approval`] |
//! | `internal/reconcile` | [`reconcile`] |
//! | `internal/execution` | [`execution`] |
//! | `internal/claims` | [`claims`] |
//! | `internal/eventsourcing` (the pure half) | [`eventsourcing`] |
//!
//! Event and action names match Go's so the two can be read side by side, and the
//! codecs read and write the JSON Go stores, so either binary can open the other's
//! database.
#![forbid(unsafe_code)]

pub mod approval;
pub mod claims;
pub mod domain;
pub mod events;
pub mod eventsourcing;
pub mod execution;
mod json;
pub mod reconcile;
