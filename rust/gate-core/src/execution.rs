//! One execution's lifecycle (Go: `internal/execution`): init, phases, per-stack
//! ticks and the terminal outcome, on stream `run:<execID>`. It carries no gate
//! state; the gate lives in [`reconcile`](crate::reconcile).

mod codec;
mod decide;
mod evolve;
mod state;

pub use codec::{Execution, marshal_event, marshal_snapshot, unmarshal_event, unmarshal_snapshot};
pub use decide::decide;
pub use evolve::evolve;
pub use state::*;
