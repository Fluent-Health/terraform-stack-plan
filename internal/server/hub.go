package server

import "sync"

// hub fans out log chunks to live (SSE) subscribers, keyed by "exec|stack".
// publish is non-blocking — a slow viewer drops chunks; the on-disk buffer and
// the object-store offload keep the full record.
type hub struct {
	mu   sync.Mutex
	subs map[string]map[chan string]struct{}
}

func newHub() *hub {
	return &hub{subs: map[string]map[chan string]struct{}{}}
}

// subscribe registers a subscriber for key and returns its receive channel plus
// an idempotent unsubscribe. The channel is buffered; the owning reader calls
// unsubscribe (once) when it stops reading.
func (h *hub) subscribe(key string) (<-chan string, func()) {
	ch := make(chan string, 256)
	h.mu.Lock()
	if h.subs[key] == nil {
		h.subs[key] = map[chan string]struct{}{}
	}
	h.subs[key][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if set := h.subs[key]; set != nil {
				delete(set, ch)
				if len(set) == 0 {
					delete(h.subs, key)
				}
			}
			h.mu.Unlock()
		})
	}
}

// publish delivers data to every current subscriber of key, dropping on a full
// channel (never blocks).
func (h *hub) publish(key, data string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[key] {
		select {
		case ch <- data:
		default:
		}
	}
}
