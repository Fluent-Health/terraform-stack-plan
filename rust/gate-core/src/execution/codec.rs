use serde::de::DeserializeOwned;

use crate::eventsourcing::{CodecError, Decider};

use super::{
    Event, Failed, PhaseChanged, StackStatusChanged, StacksAnnotated, Started, State, Succeeded,
    Superseded, evolve,
};

/// Encodes an event as its (type tag, JSON) pair.
pub fn marshal_event(e: &Event) -> Result<(&'static str, Vec<u8>), CodecError> {
    let (tag, data) = match e {
        Event::Started(v) => ("Started", serde_json::to_vec(v)),
        Event::PhaseChanged(v) => ("PhaseChanged", serde_json::to_vec(v)),
        Event::StackStatusChanged(v) => ("StackStatusChanged", serde_json::to_vec(v)),
        Event::Failed(v) => ("Failed", serde_json::to_vec(v)),
        Event::Succeeded(v) => ("Succeeded", serde_json::to_vec(v)),
        Event::StacksAnnotated(v) => ("StacksAnnotated", serde_json::to_vec(v)),
        Event::Superseded(v) => ("Superseded", serde_json::to_vec(v)),
    };
    Ok((tag, data?))
}

/// Decodes a stored (type tag, data) pair.
pub fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Event, CodecError> {
    fn de<T: DeserializeOwned + Into<Event>>(data: &[u8]) -> Result<Event, CodecError> {
        Ok(serde_json::from_slice::<T>(data)?.into())
    }
    match tag {
        "Started" => de::<Started>(data),
        "PhaseChanged" => de::<PhaseChanged>(data),
        "StackStatusChanged" => de::<StackStatusChanged>(data),
        "Failed" => de::<Failed>(data),
        "Succeeded" => de::<Succeeded>(data),
        "StacksAnnotated" => de::<StacksAnnotated>(data),
        "Superseded" => de::<Superseded>(data),
        _ => Err(CodecError::UnknownTag {
            aggregate: "execution",
            tag: tag.to_string(),
        }),
    }
}

/// Encodes a folded state.
pub fn marshal_snapshot(s: &State) -> Result<Vec<u8>, CodecError> {
    Ok(serde_json::to_vec(s)?)
}

/// Decodes a stored snapshot.
pub fn unmarshal_snapshot(data: &[u8]) -> Result<State, CodecError> {
    Ok(serde_json::from_slice(data)?)
}

/// The execution aggregate, for the store's decider host.
#[derive(Debug, Clone, Copy)]
pub struct Execution;

impl Decider for Execution {
    type State = State;
    type Event = Event;

    fn initial() -> State {
        State::default()
    }
    fn evolve(state: State, event: &Event) -> State {
        evolve(state, event)
    }
    fn marshal_event(event: &Event) -> Result<(&'static str, Vec<u8>), CodecError> {
        marshal_event(event)
    }
    fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Event, CodecError> {
        unmarshal_event(tag, data)
    }
    fn marshal_snapshot(state: &State) -> Result<Vec<u8>, CodecError> {
        marshal_snapshot(state)
    }
    fn unmarshal_snapshot(data: &[u8]) -> Result<State, CodecError> {
        unmarshal_snapshot(data)
    }
}

#[cfg(test)]
mod tests;
