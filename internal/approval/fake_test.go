package approval

import (
	"context"
	"testing"
)

func TestFakeRequestGrantIdempotent(t *testing.T) {
	f := NewFake()
	req := Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"}
	g1, err := f.RequestGrant(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if g1.State != StateAwaiting {
		t.Errorf("new grant state = %s, want AWAITING", g1.State)
	}
	g2, _ := f.RequestGrant(context.Background(), req)
	if g2.Name != g1.Name {
		t.Errorf("re-request created a new grant (%s vs %s) — must reuse", g2.Name, g1.Name)
	}
}

func TestFakeListFiltersByClassTarget(t *testing.T) {
	f := NewFake()
	_, _ = f.RequestGrant(context.Background(), Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"})
	_, _ = f.RequestGrant(context.Background(), Request{Class: "iam", Target: "proj-b", PR: 7, Environment: "staging"})
	got, _ := f.ListGrants(context.Background(), "iam", "proj-a")
	if len(got) != 1 || got[0].Request.Target != "proj-a" {
		t.Fatalf("ListGrants(iam,proj-a) = %+v", got)
	}
}

func TestFakeApproveAndRevoke(t *testing.T) {
	f := NewFake()
	req := Request{Class: "iam", Target: "proj-a", PR: 7, Environment: "staging"}
	_, _ = f.RequestGrant(context.Background(), req)
	f.Approve(req)
	got, _ := f.ListGrants(context.Background(), "iam", "proj-a")
	if got[0].State != StateActive {
		t.Errorf("after Approve, state = %s, want ACTIVE", got[0].State)
	}
	if err := f.Revoke(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got, _ = f.ListGrants(context.Background(), "iam", "proj-a")
	if got[0].State != StateRevoked {
		t.Errorf("after Revoke, state = %s, want REVOKED", got[0].State)
	}
}

func TestFakeImplementsBackend(t *testing.T) {
	var _ Backend = (*Fake)(nil)
}
