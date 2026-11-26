//! The pure half of the generic decider host (Go: `internal/eventsourcing`). Each
//! aggregate supplies its initial state, its fold and its codecs; the store crate
//! loads a stream (snapshot, then the tail folded through [`Decider::evolve`]) and
//! appends to it. `decide` and `react` stay with each aggregate, because their
//! signatures differ.

/// A stored event or snapshot could not be encoded or decoded.
#[derive(Debug, thiserror::Error)]
pub enum CodecError {
    /// The stored type tag names no event of this aggregate.
    #[error("{aggregate}: unknown event tag {tag:?}")]
    UnknownTag {
        aggregate: &'static str,
        tag: String,
    },
    /// A snapshot names no gate state.
    #[error("{aggregate}: unknown gate kind {kind:?}")]
    UnknownGateKind {
        aggregate: &'static str,
        kind: String,
    },
    #[error(transparent)]
    Json(#[from] serde_json::Error),
}

/// What the store needs from an aggregate to load and append its stream.
pub trait Decider {
    /// The folded state.
    type State;
    /// A past-tense fact.
    type Event;

    /// The state of an empty stream.
    fn initial() -> Self::State;
    /// Folds one event into the state.
    fn evolve(state: Self::State, event: &Self::Event) -> Self::State;
    /// Encodes an event as a (type tag, JSON) pair.
    fn marshal_event(event: &Self::Event) -> Result<(&'static str, Vec<u8>), CodecError>;
    /// Decodes a stored (type tag, data) pair.
    fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Self::Event, CodecError>;
    /// Encodes the folded state.
    fn marshal_snapshot(state: &Self::State) -> Result<Vec<u8>, CodecError>;
    /// Decodes a stored snapshot.
    fn unmarshal_snapshot(data: &[u8]) -> Result<Self::State, CodecError>;
}
