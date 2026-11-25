use serde::de::DeserializeOwned;

use crate::eventsourcing::{CodecError, Decider};

use super::{
    ClaimAcquired, ClaimReleased, ClaimRenewed, ClaimSet, ClaimStackReleased, Event, evolve,
};

/// Encodes an event as its (type tag, JSON) pair.
pub fn marshal_event(e: &Event) -> Result<(&'static str, Vec<u8>), CodecError> {
    let (tag, data) = match e {
        Event::ClaimAcquired(v) => ("ClaimAcquired", serde_json::to_vec(v)),
        Event::ClaimRenewed(v) => ("ClaimRenewed", serde_json::to_vec(v)),
        Event::ClaimReleased(v) => ("ClaimReleased", serde_json::to_vec(v)),
        Event::ClaimStackReleased(v) => ("ClaimStackReleased", serde_json::to_vec(v)),
    };
    Ok((tag, data?))
}

/// Decodes a stored (type tag, data) pair.
pub fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Event, CodecError> {
    fn de<T: DeserializeOwned + Into<Event>>(data: &[u8]) -> Result<Event, CodecError> {
        Ok(serde_json::from_slice::<T>(data)?.into())
    }
    match tag {
        "ClaimAcquired" => de::<ClaimAcquired>(data),
        "ClaimRenewed" => de::<ClaimRenewed>(data),
        "ClaimReleased" => de::<ClaimReleased>(data),
        "ClaimStackReleased" => de::<ClaimStackReleased>(data),
        _ => Err(CodecError::UnknownTag {
            aggregate: "claims",
            tag: tag.to_string(),
        }),
    }
}

/// Encodes the folded ledger: a JSON object of stack path to claim.
pub fn marshal_snapshot(cs: &ClaimSet) -> Result<Vec<u8>, CodecError> {
    Ok(serde_json::to_vec(cs)?)
}

/// Decodes a stored ledger. Go writes `null` for a nil map.
pub fn unmarshal_snapshot(data: &[u8]) -> Result<ClaimSet, CodecError> {
    Ok(serde_json::from_slice::<Option<ClaimSet>>(data)?.unwrap_or_default())
}

/// The claim-ledger aggregate, for the store's decider host.
#[derive(Debug, Clone, Copy)]
pub struct Claims;

impl Decider for Claims {
    type State = ClaimSet;
    type Event = Event;

    fn initial() -> ClaimSet {
        ClaimSet::new()
    }
    fn evolve(state: ClaimSet, event: &Event) -> ClaimSet {
        evolve(state, event)
    }
    fn marshal_event(event: &Event) -> Result<(&'static str, Vec<u8>), CodecError> {
        marshal_event(event)
    }
    fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Event, CodecError> {
        unmarshal_event(tag, data)
    }
    fn marshal_snapshot(state: &ClaimSet) -> Result<Vec<u8>, CodecError> {
        marshal_snapshot(state)
    }
    fn unmarshal_snapshot(data: &[u8]) -> Result<ClaimSet, CodecError> {
        unmarshal_snapshot(data)
    }
}

#[cfg(test)]
mod tests;
