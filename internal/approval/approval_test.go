package approval

import "testing"

func TestGrantStateOpen(t *testing.T) {
	open := []GrantState{StateAwaiting, StateActivating, StateActive}
	closed := []GrantState{StateDenied, StateRevoked, StateExpired, GrantState("")}
	for _, s := range open {
		if !s.Open() {
			t.Errorf("%s should be open", s)
		}
	}
	for _, s := range closed {
		if s.Open() {
			t.Errorf("%s should be closed", s)
		}
	}
}
