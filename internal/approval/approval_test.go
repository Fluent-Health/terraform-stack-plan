package approval

import (
	"strings"
	"testing"
)

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

func TestSlotCollisionErrorMessage(t *testing.T) {
	e := &SlotCollisionError{
		BlockingGrant: Grant{Request: Request{PR: 7, Environment: "nonprod"}},
	}
	msg := e.Error()
	if !strings.Contains(msg, "7") {
		t.Errorf("error = %q; want PR number 7", msg)
	}
	if !strings.Contains(msg, "nonprod") {
		t.Errorf("error = %q; want environment nonprod", msg)
	}
}

func TestSlotCollisionIsError(t *testing.T) {
	var err error = &SlotCollisionError{}
	if err == nil {
		t.Error("SlotCollisionError must satisfy error interface")
	}
}
