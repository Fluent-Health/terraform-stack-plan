package execution

import (
	"encoding/json"
	"fmt"
)

// MarshalEvent serializes a domain Event to a neutral (typeTag, JSON) pair. The
// tag is the variant's Go type name. Keep this switch and UnmarshalEvent in sync
// with the Event variants in state.go (the round-trip-every-variant test enforces it).
func MarshalEvent(e Event) (string, []byte, error) {
	tag := eventTag(e)
	if tag == "" {
		return "", nil, fmt.Errorf("execution: no codec tag for event %T", e)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", nil, err
	}
	return tag, data, nil
}

func eventTag(e Event) string {
	switch e.(type) {
	case Started:
		return "Started"
	case PhaseChanged:
		return "PhaseChanged"
	case StackStatusChanged:
		return "StackStatusChanged"
	case Failed:
		return "Failed"
	case Succeeded:
		return "Succeeded"
	case StacksAnnotated:
		return "StacksAnnotated"
	case Superseded:
		return "Superseded"
	default:
		return ""
	}
}

// UnmarshalEvent reverses MarshalEvent.
func UnmarshalEvent(tag string, data []byte) (Event, error) {
	switch tag {
	case "Started":
		return unmarshalInto[Started](data)
	case "PhaseChanged":
		return unmarshalInto[PhaseChanged](data)
	case "StackStatusChanged":
		return unmarshalInto[StackStatusChanged](data)
	case "Failed":
		return unmarshalInto[Failed](data)
	case "Succeeded":
		return unmarshalInto[Succeeded](data)
	case "StacksAnnotated":
		return unmarshalInto[StacksAnnotated](data)
	case "Superseded":
		return unmarshalInto[Superseded](data)
	default:
		return nil, fmt.Errorf("execution: unknown event tag %q", tag)
	}
}

func unmarshalInto[T any](data []byte) (Event, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return any(v).(Event), nil
}

// MarshalSnapshot serializes a folded State to JSON.
func MarshalSnapshot(s State) ([]byte, error) { return json.Marshal(s) }

// UnmarshalSnapshot reverses MarshalSnapshot.
func UnmarshalSnapshot(b []byte) (State, error) {
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	return s, nil
}
