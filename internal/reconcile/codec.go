package reconcile

import (
	"encoding/json"
	"fmt"
)

// MarshalEvent serializes a domain Event to a neutral (typeTag, JSON) pair. The
// tag is the variant's Go type name; the data is its JSON. Neutral types only —
// no store dependency. Keep this switch and decodeEvent in sync with the Event
// variants in event.go (the round-trip-every-variant test enforces it).
func MarshalEvent(e Event) (string, []byte, error) {
	tag := eventTag(e)
	if tag == "" {
		return "", nil, fmt.Errorf("reconcile: no codec tag for event %T", e)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", nil, err
	}
	return tag, data, nil
}

func eventTag(e Event) string {
	switch e.(type) {
	case ExecutionStarted:
		return "ExecutionStarted"
	case PhaseChanged:
		return "PhaseChanged"
	case StackStatusChanged:
		return "StackStatusChanged"
	case ExecutionFailed:
		return "ExecutionFailed"
	case StacksClassified:
		return "StacksClassified"
	case Classified:
		return "Classified"
	case GrantObserved:
		return "GrantObserved"
	case GrantCleared:
		return "GrantCleared"
	case GateTargetRequested:
		return "GateTargetRequested"
	case GateSatisfied:
		return "GateSatisfied"
	case GateBlocked:
		return "GateBlocked"
	case TargetRevoked:
		return "TargetRevoked"
	case GatePassed:
		return "GatePassed"
	case GateReleased:
		return "GateReleased"
	case ClaimReleased:
		return "ClaimReleased"
	case PRClosedRecorded:
		return "PRClosedRecorded"
	default:
		return ""
	}
}

// UnmarshalEvent reverses MarshalEvent.
func UnmarshalEvent(tag string, data []byte) (Event, error) {
	switch tag {
	case "ExecutionStarted":
		return unmarshalInto[ExecutionStarted](data)
	case "PhaseChanged":
		return unmarshalInto[PhaseChanged](data)
	case "StackStatusChanged":
		return unmarshalInto[StackStatusChanged](data)
	case "ExecutionFailed":
		return unmarshalInto[ExecutionFailed](data)
	case "StacksClassified":
		return unmarshalInto[StacksClassified](data)
	case "Classified":
		return unmarshalInto[Classified](data)
	case "GrantObserved":
		return unmarshalInto[GrantObserved](data)
	case "GrantCleared":
		return unmarshalInto[GrantCleared](data)
	case "GateTargetRequested":
		return unmarshalInto[GateTargetRequested](data)
	case "GateSatisfied":
		return unmarshalInto[GateSatisfied](data)
	case "GateBlocked":
		return unmarshalInto[GateBlocked](data)
	case "TargetRevoked":
		return unmarshalInto[TargetRevoked](data)
	case "GatePassed":
		return unmarshalInto[GatePassed](data)
	case "GateReleased":
		return unmarshalInto[GateReleased](data)
	case "ClaimReleased":
		return unmarshalInto[ClaimReleased](data)
	case "PRClosedRecorded":
		return unmarshalInto[PRClosedRecorded](data)
	default:
		return nil, fmt.Errorf("reconcile: unknown event tag %q", tag)
	}
}

func unmarshalInto[T any](data []byte) (Event, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	// T is always an Event variant (value type); assert through any.
	return any(v).(Event), nil
}

// --- snapshot ---

type snapshotDTO struct {
	PR          int             `json:"pr"`
	Environment string          `json:"environment"`
	Exec        Execution       `json:"exec"`
	GateKind    string          `json:"gate_kind"`
	Gate        json.RawMessage `json:"gate"`
}

// MarshalSnapshot serializes a folded ChangeSet (the GateState sum type is encoded
// with a discriminator). Neutral []byte — no store dependency.
func MarshalSnapshot(cs ChangeSet) ([]byte, error) {
	kind, err := gateStateKind(cs.Gate)
	if err != nil {
		return nil, err
	}
	gateJSON, err := json.Marshal(cs.Gate)
	if err != nil {
		return nil, err
	}
	return json.Marshal(snapshotDTO{
		PR: cs.PR, Environment: cs.Environment, Exec: cs.Exec,
		GateKind: kind, Gate: gateJSON,
	})
}

func gateStateKind(g GateState) (string, error) {
	switch g.(type) {
	case NotClassified, nil:
		return "NotClassified", nil
	case Clean:
		return "Clean", nil
	case Pending:
		return "Pending", nil
	case Satisfied:
		return "Satisfied", nil
	case Blocked:
		return "Blocked", nil
	default:
		return "", fmt.Errorf("reconcile: no snapshot kind for gate %T", g)
	}
}

// UnmarshalSnapshot reverses MarshalSnapshot.
func UnmarshalSnapshot(b []byte) (ChangeSet, error) {
	var dto snapshotDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		return ChangeSet{}, err
	}
	cs := ChangeSet{PR: dto.PR, Environment: dto.Environment, Exec: dto.Exec}
	gate, err := decodeGate(dto.GateKind, dto.Gate)
	if err != nil {
		return ChangeSet{}, err
	}
	cs.Gate = gate
	return cs, nil
}

func decodeGate(kind string, data json.RawMessage) (GateState, error) {
	switch kind {
	case "NotClassified":
		return NotClassified{}, nil
	case "Clean":
		return Clean{}, nil
	case "Pending":
		var v Pending
		return v, json.Unmarshal(data, &v)
	case "Satisfied":
		var v Satisfied
		return v, json.Unmarshal(data, &v)
	case "Blocked":
		var v Blocked
		return v, json.Unmarshal(data, &v)
	default:
		return nil, fmt.Errorf("reconcile: unknown gate kind %q", kind)
	}
}
