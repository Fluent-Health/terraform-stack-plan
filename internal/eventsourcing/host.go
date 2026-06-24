// Package eventsourcing is a generic, domain-agnostic decider host over
// store.EventStore: Load (snapshot + replay-tail via Evolve) and Append (encode +
// optimistic append + snapshot), parameterized per aggregate by a Decider value.
package eventsourcing

import "github.com/Fluent-Health/terraform-stack-plan/internal/store"

// Decider bundles the per-aggregate funcs the host needs. Domains supply these as
// plain funcs; the host owns no domain types. Decide/React/action-execution and any
// fixpoint stay in the domain shell — only the event-sourcing plumbing is generic.
type Decider[S, E any] struct {
	Initial           func() S
	Evolve            func(S, E) S
	MarshalEvent      func(E) (typeTag string, data []byte, err error)
	UnmarshalEvent    func(typeTag string, data []byte) (E, error)
	MarshalSnapshot   func(S) ([]byte, error)
	UnmarshalSnapshot func([]byte) (S, error)
}

// Load reconstructs a stream's state: latest snapshot folded forward over any
// newer events via Evolve. Returns the folded state and the stream's current
// version (Initial() at version 0 for an empty stream).
func (d Decider[S, E]) Load(es *store.EventStore, streamID string) (S, int, error) {
	state := d.Initial()
	snap, snapVer, ok, err := es.LoadSnapshot(streamID)
	if err != nil {
		return state, 0, err
	}
	if ok {
		s, derr := d.UnmarshalSnapshot(snap)
		if derr != nil {
			return state, 0, derr
		}
		state = s
	}
	stored, version, err := es.Load(streamID)
	if err != nil {
		return state, 0, err
	}
	for i, se := range stored {
		if i+1 <= snapVer { // events are 1-based; skip those already in the snapshot
			continue
		}
		ev, derr := d.UnmarshalEvent(se.Type, se.Data)
		if derr != nil {
			return state, 0, derr
		}
		state = d.Evolve(state, ev)
	}
	return state, version, nil
}

// Append encodes evs, appends them at expectedVersion (optimistic concurrency via
// store.EventStore), then snapshots newState at the resulting version. No-op when
// evs is empty. Returns store.ErrConcurrencyConflict on a version mismatch.
func (d Decider[S, E]) Append(es *store.EventStore, streamID string, expectedVersion int, evs []E, newState S) error {
	if len(evs) == 0 {
		return nil
	}
	stored := make([]store.StoredEvent, 0, len(evs))
	for _, e := range evs {
		tag, data, err := d.MarshalEvent(e)
		if err != nil {
			return err
		}
		stored = append(stored, store.StoredEvent{Type: tag, Data: data})
	}
	if err := es.Append(streamID, expectedVersion, stored); err != nil {
		return err
	}
	snap, err := d.MarshalSnapshot(newState)
	if err != nil {
		return err
	}
	return es.SaveSnapshot(streamID, expectedVersion+len(evs), snap)
}
