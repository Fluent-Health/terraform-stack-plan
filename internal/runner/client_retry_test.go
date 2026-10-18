package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// TestUpdateRetriesTransient5xx pins the lost-tick fix: serve 500'd a stack's
// terminal tick once (SQLITE_BUSY), the runner dropped it, and the apply check
// hung in `report` forever. A 5xx is retried; a 4xx is not.
func TestUpdateRetriesTransient5xx(t *testing.T) {
	defer func(a int, b time.Duration) { updateAttempts, updateBackoff = a, b }(updateAttempts, updateBackoff)
	updateBackoff = time.Millisecond

	for _, tc := range []struct {
		name      string
		codes     []int
		wantCalls int
		wantErr   bool
	}{
		{"one 500 then ok", []int{500, 200}, 2, false},
		{"persistent 500", []int{500, 500, 500}, 3, true},
		{"4xx is final", []int{400}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				code := tc.codes[min(calls, len(tc.codes)-1)]
				calls++
				mu.Unlock()
				w.WriteHeader(code)
			}))
			defer srv.Close()

			err := NewClient(srv.URL).Update(context.Background(),
				events.Update{ID: "e1", Stack: "stacks/c", Status: events.StatusSafe})
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if calls != tc.wantCalls {
				t.Errorf("calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
