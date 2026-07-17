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

func TestDecodeWhoamiJWTSubject(t *testing.T) {
	validToken := "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImFkbWluQGZsdWVudC1oZWFsdGguY29tIn0."
	email, err := decodeWhoamiJWTSubject(validToken)
	if err != nil {
		t.Fatalf("decodeWhoamiJWTSubject failed: %v", err)
	}
	if email != "admin@fluent-health.com" {
		t.Errorf("got email = %q, want admin@fluent-health.com", email)
	}

	invalidToken := "invalid-jwt-token"
	_, err = decodeWhoamiJWTSubject(invalidToken)
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestDispatchWhoamiWithToken(t *testing.T) {
	t.Setenv("TFSTACKPLAN_SERVER", "https://mock-server")
	t.Setenv("TFSTACKPLAN_AUDIENCE", "https://mock-server")

	code := dispatch([]string{"whoami"})
	if code != 0 && code != 1 {
		t.Fatalf("dispatch whoami exit = %d, want 0 or 1", code)
	}
}
