package claims

import (
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

func TestDecideAcquireComputesExpiry(t *testing.T) {
	got := Decide(Empty(), AcquireClaim{PR: 7, Stacks: []string{"a", "b"}, Now: t0})
	want := []Event{ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRenewComputesExpiry(t *testing.T) {
	s := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a"}, ExpiresAt: t0.Add(Lease())})
	got := Decide(s, RenewClaim{PR: 7, Now: t0})
	want := []Event{ClaimRenewed{PR: 7, ExpiresAt: t0.Add(Lease())}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideRelease(t *testing.T) {
	s := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a"}, ExpiresAt: t0.Add(Lease())})
	got := Decide(s, ReleaseClaim{PR: 7})
	want := []Event{ClaimReleased{PR: 7}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestDecideReleaseStack(t *testing.T) {
	s := Evolve(Empty(), ClaimAcquired{PR: 7, Stacks: []string{"a", "b"}, ExpiresAt: t0.Add(Lease())})
	got := Decide(s, ReleaseClaimStack{PR: 7, Stack: "a"})
	want := []Event{ClaimStackReleased{PR: 7, Stack: "a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
