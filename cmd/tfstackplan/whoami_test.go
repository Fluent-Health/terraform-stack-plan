package main

import (
	"testing"
)

func TestDispatchWhoamiAnonymous(t *testing.T) {
	t.Setenv("TFSTACKPLAN_SERVER", "")
	t.Setenv("TFSTACKPLAN_AUDIENCE", "")

	code := dispatch([]string{"whoami"})
	if code != 0 {
		t.Fatalf("dispatch whoami exit = %d, want 0", code)
	}
}

func TestDispatchRunWhoamiAnonymous(t *testing.T) {
	t.Setenv("TFSTACKPLAN_SERVER", "")
	t.Setenv("TFSTACKPLAN_AUDIENCE", "")

	code := dispatch([]string{"run", "whoami"})
	if code != 0 {
		t.Fatalf("dispatch run whoami exit = %d, want 0", code)
	}
}
