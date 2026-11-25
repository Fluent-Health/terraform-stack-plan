use std::collections::BTreeMap;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::eventsourcing::{CodecError, Decider};
use crate::json::nullable;

use super::{ChangeSet, CheckOverride, Event, GateState, Run, evolve};

const AGGREGATE: &str = "reconcile";

/// Encodes an event as its (type tag, JSON) pair. The tag is the variant name.
pub fn marshal_event(e: &Event) -> Result<(&'static str, Vec<u8>), CodecError> {
    let (tag, data) = match e {
        Event::Classified(v) => ("Classified", serde_json::to_vec(v)),
        Event::GrantObserved(v) => ("GrantObserved", serde_json::to_vec(v)),
        Event::GrantCleared(v) => ("GrantCleared", serde_json::to_vec(v)),
        Event::GateTargetRequested(v) => ("GateTargetRequested", serde_json::to_vec(v)),
        Event::GateSatisfied(v) => ("GateSatisfied", serde_json::to_vec(v)),
        Event::GateBlocked(v) => ("GateBlocked", serde_json::to_vec(v)),
        Event::TargetRevoked(v) => ("TargetRevoked", serde_json::to_vec(v)),
        Event::GatePassed(v) => ("GatePassed", serde_json::to_vec(v)),
        Event::GateReleased(v) => ("GateReleased", serde_json::to_vec(v)),
        Event::ClaimReleased(v) => ("ClaimReleased", serde_json::to_vec(v)),
        Event::PRClosedRecorded(v) => ("PRClosedRecorded", serde_json::to_vec(v)),
        Event::RunQueued(v) => ("RunQueued", serde_json::to_vec(v)),
        Event::RunStarted(v) => ("RunStarted", serde_json::to_vec(v)),
        Event::RunStartFailed(v) => ("RunStartFailed", serde_json::to_vec(v)),
        Event::RunSuperseded(v) => ("RunSuperseded", serde_json::to_vec(v)),
        Event::RunCompleted(v) => ("RunCompleted", serde_json::to_vec(v)),
        Event::RunAdopted(v) => ("RunAdopted", serde_json::to_vec(v)),
        Event::AdminGrantReleased(v) => ("AdminGrantReleased", serde_json::to_vec(v)),
        Event::AdminExecutionCancelled(v) => ("AdminExecutionCancelled", serde_json::to_vec(v)),
        Event::AdminGateSatisfied(v) => ("AdminGateSatisfied", serde_json::to_vec(v)),
        Event::AdminCheckOverridden(v) => ("AdminCheckOverridden", serde_json::to_vec(v)),
    };
    Ok((tag, data?))
}

/// Decodes a stored (type tag, data) pair.
pub fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Event, CodecError> {
    fn de<T: DeserializeOwned + Into<Event>>(data: &[u8]) -> Result<Event, CodecError> {
        Ok(serde_json::from_slice::<T>(data)?.into())
    }
    use super::*;
    match tag {
        "Classified" => de::<Classified>(data),
        "GrantObserved" => de::<GrantObserved>(data),
        "GrantCleared" => de::<GrantCleared>(data),
        "GateTargetRequested" => de::<GateTargetRequested>(data),
        "GateSatisfied" => de::<GateSatisfied>(data),
        "GateBlocked" => de::<GateBlocked>(data),
        "TargetRevoked" => de::<TargetRevoked>(data),
        "GatePassed" => de::<GatePassed>(data),
        "GateReleased" => de::<GateReleased>(data),
        "ClaimReleased" => de::<ClaimReleased>(data),
        "PRClosedRecorded" => de::<PRClosedRecorded>(data),
        "RunQueued" => de::<RunQueued>(data),
        "RunStarted" => de::<RunStarted>(data),
        "RunStartFailed" => de::<RunStartFailed>(data),
        "RunSuperseded" => de::<RunSuperseded>(data),
        "RunCompleted" => de::<RunCompleted>(data),
        "RunAdopted" => de::<RunAdopted>(data),
        "AdminGrantReleased" => de::<AdminGrantReleased>(data),
        "AdminExecutionCancelled" => de::<AdminExecutionCancelled>(data),
        "AdminGateSatisfied" => de::<AdminGateSatisfied>(data),
        "AdminCheckOverridden" => de::<AdminCheckOverridden>(data),
        _ => Err(CodecError::UnknownTag {
            aggregate: AGGREGATE,
            tag: tag.to_string(),
        }),
    }
}

/// The snapshot layout Go writes: the gate sum type as a kind plus its JSON.
#[derive(Serialize)]
struct SnapshotOut<'a> {
    pr: i64,
    environment: &'a str,
    gate_kind: &'static str,
    gate: Box<RawValue>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    runs: &'a BTreeMap<String, Run>,
    #[serde(skip_serializing_if = "Option::is_none")]
    check_override: &'a Option<CheckOverride>,
}

#[derive(Deserialize)]
struct SnapshotIn {
    #[serde(default)]
    pr: i64,
    #[serde(default)]
    environment: String,
    gate_kind: String,
    #[serde(default)]
    gate: Option<Box<RawValue>>,
    #[serde(default, deserialize_with = "nullable")]
    runs: BTreeMap<String, Run>,
    #[serde(default)]
    check_override: Option<CheckOverride>,
}

/// Encodes a folded change set.
pub fn marshal_snapshot(cs: &ChangeSet) -> Result<Vec<u8>, CodecError> {
    let (gate_kind, gate) = match &cs.gate {
        GateState::NotClassified => ("NotClassified", serde_json::to_string(&Empty {})?),
        GateState::Clean => ("Clean", serde_json::to_string(&Empty {})?),
        GateState::Pending(v) => ("Pending", serde_json::to_string(v)?),
        GateState::Satisfied(v) => ("Satisfied", serde_json::to_string(v)?),
        GateState::Blocked(v) => ("Blocked", serde_json::to_string(v)?),
    };
    Ok(serde_json::to_vec(&SnapshotOut {
        pr: cs.pr,
        environment: &cs.environment,
        gate_kind,
        gate: RawValue::from_string(gate)?,
        runs: &cs.runs,
        check_override: &cs.check_override,
    })?)
}

/// Go marshals an empty struct as `{}`.
#[derive(Serialize)]
struct Empty {}

/// Decodes a stored change set snapshot.
pub fn unmarshal_snapshot(data: &[u8]) -> Result<ChangeSet, CodecError> {
    let dto: SnapshotIn = serde_json::from_slice(data)?;
    let raw = dto.gate.as_ref().map_or("null", |g| g.get());
    let gate = match dto.gate_kind.as_str() {
        "NotClassified" => GateState::NotClassified,
        "Clean" => GateState::Clean,
        "Pending" => GateState::Pending(serde_json::from_str(raw)?),
        "Satisfied" => GateState::Satisfied(serde_json::from_str(raw)?),
        "Blocked" => GateState::Blocked(serde_json::from_str(raw)?),
        kind => {
            return Err(CodecError::UnknownGateKind {
                aggregate: AGGREGATE,
                kind: kind.to_string(),
            });
        }
    };
    Ok(ChangeSet {
        pr: dto.pr,
        environment: dto.environment,
        gate,
        check_override: dto.check_override,
        runs: dto.runs,
    })
}

/// The gate aggregate, for the store's decider host.
#[derive(Debug, Clone, Copy)]
pub struct Gate;

impl Decider for Gate {
    type State = ChangeSet;
    type Event = Event;

    fn initial() -> ChangeSet {
        ChangeSet::default()
    }
    fn evolve(state: ChangeSet, event: &Event) -> ChangeSet {
        evolve(state, event)
    }
    fn marshal_event(event: &Event) -> Result<(&'static str, Vec<u8>), CodecError> {
        marshal_event(event)
    }
    fn unmarshal_event(tag: &str, data: &[u8]) -> Result<Event, CodecError> {
        unmarshal_event(tag, data)
    }
    fn marshal_snapshot(state: &ChangeSet) -> Result<Vec<u8>, CodecError> {
        marshal_snapshot(state)
    }
    fn unmarshal_snapshot(data: &[u8]) -> Result<ChangeSet, CodecError> {
        unmarshal_snapshot(data)
    }
}

#[cfg(test)]
mod tests;
