//! The closed registry of stable, machine-readable problem codes (`internal/codes`).

use std::fmt;

/// One distinct problem condition, namespaced `NAMESPACE-###`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct Code(pub &'static str);

/// The PR never finalized a plan; apply fails closed.
pub const GATE_NOT_CLASSIFIED: Code = Code("GATE-001");
/// A required grant is not yet active.
pub const GATE_NOT_SATISFIED: Code = Code("GATE-002");
/// Gate state could not be freshly confirmed.
pub const GATE_UNCONFIRMABLE: Code = Code("GATE-003");
/// The approval server itself is unreachable.
pub const GATE_UNREACHABLE: Code = Code("GATE-004");
/// A decoded payload carried a status outside the known set.
pub const UNKNOWN_STATUS: Code = Code("WIRE-001");
/// Unexpected server-side error.
pub const INTERNAL: Code = Code("SRV-001");

/// Every registered code.
pub fn all() -> Vec<Code> {
    vec![
        GATE_NOT_CLASSIFIED,
        GATE_NOT_SATISFIED,
        GATE_UNCONFIRMABLE,
        GATE_UNREACHABLE,
        UNKNOWN_STATUS,
        INTERNAL,
    ]
}

/// A coded error: a stable code and a message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error {
    /// Code.
    pub code: Code,
    /// Message.
    pub msg: String,
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}: {}", self.code.0, self.msg)
    }
}

impl std::error::Error for Error {}

/// A coded error with a formatted message.
pub fn error(code: Code, msg: impl Into<String>) -> Error {
    Error { code, msg: msg.into() }
}

#[cfg(test)]
mod tests;
