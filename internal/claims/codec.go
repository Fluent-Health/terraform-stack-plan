package claims

import (
	"encoding/json"
	"fmt"
)

// MarshalEvent serializes a domain Event to a neutral (typeTag, JSON) pair. The
// tag is the variant's Go type name; the data is its JSON. No store dependency.
// Keep this switch and UnmarshalEvent in sync with the Event variants in
// claim.go (the round-trip-every-variant test enforces it).
func MarshalEvent(e Event) (string, []byte, error) {
	tag := eventTag(e)
	if tag == "" {
		return "", nil, fmt.Errorf("claims: no codec tag for event %T", e)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", nil, err
	}
	return tag, data, nil
}

func eventTag(e Event) string {
	switch e.(type) {
	case ClaimAcquired:
		return "ClaimAcquired"
	case ClaimRenewed:
		return "ClaimRenewed"
	case ClaimReleased:
		return "ClaimReleased"
	case ClaimStackReleased:
		return "ClaimStackReleased"
	default:
		return ""
	}
}

// UnmarshalEvent reverses MarshalEvent.
func UnmarshalEvent(tag string, data []byte) (Event, error) {
	switch tag {
	case "ClaimAcquired":
		return unmarshalInto[ClaimAcquired](data)
	case "ClaimRenewed":
		return unmarshalInto[ClaimRenewed](data)
	case "ClaimReleased":
		return unmarshalInto[ClaimReleased](data)
	case "ClaimStackReleased":
		return unmarshalInto[ClaimStackReleased](data)
	default:
		return nil, fmt.Errorf("claims: unknown event tag %q", tag)
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

// MarshalSnapshot serializes a folded ClaimSet to JSON. ClaimSet is a plain map
// so it round-trips directly via encoding/json.
func MarshalSnapshot(cs ClaimSet) ([]byte, error) {
	return json.Marshal(cs)
}

// UnmarshalSnapshot reverses MarshalSnapshot.
func UnmarshalSnapshot(b []byte) (ClaimSet, error) {
	var cs ClaimSet
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}
