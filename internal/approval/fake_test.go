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

// TestFakePoolLease verifies that an empty Requester hint causes the Fake to
// pick the first unused pool entry, and that a pre-set Requester hint is
// honoured directly without touching the pool.
func TestFakePoolLease(t *testing.T) {
	ctx := context.Background()

	t.Run("pool lease picks first unused", func(t *testing.T) {
		f := NewFake()
		f.Pool = []string{"sa0", "sa1"}
		g, err := f.RequestGrant(ctx, Request{Class: "iam", Target: "proj-a", PR: 1, Environment: "e"})
		if err != nil {
			t.Fatal(err)
		}
		if g.Requester != "sa0" {
			t.Errorf("Requester = %q, want sa0", g.Requester)
		}
		// second grant for a different target — sa0 is in use, must pick sa1
		g2, err := f.RequestGrant(ctx, Request{Class: "iam", Target: "proj-b", PR: 1, Environment: "e"})
		if err != nil {
			t.Fatal(err)
		}
		if g2.Requester != "sa1" {
			t.Errorf("Requester = %q, want sa1 (sa0 in use)", g2.Requester)
		}
	})

	t.Run("requester hint is honoured", func(t *testing.T) {
		f := NewFake()
		f.Pool = []string{"sa0", "sa1"}
		g, err := f.RequestGrant(ctx, Request{Class: "iam", Target: "proj-a", PR: 2, Environment: "e", Requester: "sa0"})
		if err != nil {
			t.Fatal(err)
		}
		if g.Requester != "sa0" {
			t.Errorf("Requester = %q, want sa0 (hinted)", g.Requester)
		}
		// second grant with same hint — must reuse same pool identity
		g2, err := f.RequestGrant(ctx, Request{Class: "iam", Target: "proj-b", PR: 2, Environment: "e", Requester: "sa0"})
		if err != nil {
			t.Fatal(err)
		}
		if g2.Requester != "sa0" {
			t.Errorf("Requester = %q, want sa0 (hinted)", g2.Requester)
		}
	})

	t.Run("empty pool means no requester", func(t *testing.T) {
		f := NewFake()
		g, err := f.RequestGrant(ctx, Request{Class: "iam", Target: "proj-a", PR: 3, Environment: "e"})
		if err != nil {
			t.Fatal(err)
		}
		if g.Requester != "" {
			t.Errorf("Requester = %q, want empty (no pool)", g.Requester)
		}
	})
}
