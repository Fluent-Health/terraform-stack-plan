package server

import (
	"fmt"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// execStreamID is the event-stream id for a (pr, environment) gate lifecycle.
func execStreamID(pr int, env string) string { return fmt.Sprintf("exec:%d:%s", pr, env) }

// encodeEvents marshals domain events to the store's neutral StoredEvent rows.
func encodeEvents(evs []reconcile.Event) ([]store.StoredEvent, error) {
	out := make([]store.StoredEvent, 0, len(evs))
	for _, e := range evs {
		tag, data, err := reconcile.MarshalEvent(e)
		if err != nil {
			return nil, err
		}
		out = append(out, store.StoredEvent{Type: tag, Data: data})
	}
	return out, nil
}
