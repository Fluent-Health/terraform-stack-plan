package approval

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Fake is an in-memory Backend for tests. Grants start AWAITING; a test calls
// Approve to simulate an approver flipping a grant ACTIVE. Safe for concurrent
// use (the reconcile loop may call it while a test mutates it).
type Fake struct {
	mu     sync.Mutex
	grants map[string]*Grant // key: class|target|pr|environment
	seq    int
	// Pool is an optional list of requester identities that the Fake leases like
	// gcppam does: when req.Requester is empty and Pool is non-empty, the first
	// pool entry that is not already the Requester of an open grant is chosen.
	// Empty Pool ⇒ no requester (existing tests are unaffected).
	Pool []string
}

var _ Backend = (*Fake)(nil)

// NewFake returns an empty in-memory backend.
func NewFake() *Fake { return &Fake{grants: map[string]*Grant{}} }

func fkey(r Request) string {
	return fmt.Sprintf("%s|%s|%d|%s", r.Class, r.Target, r.PR, r.Environment)
}

func (f *Fake) RequestGrant(_ context.Context, req Request) (Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.grants[fkey(req)]; ok && g.State.Open() {
		return *g, nil
	}
	// Choose the requester: honour a pre-leased hint, otherwise pick the first
	// pool entry not already in use by an open grant.
	requester := req.Requester
	if requester == "" && len(f.Pool) > 0 {
		used := map[string]bool{}
		for _, g := range f.grants {
			if g.State.Open() && g.Requester != "" {
				used[g.Requester] = true
			}
		}
		for _, sa := range f.Pool {
			if !used[sa] {
				requester = sa
				break
			}
		}
	}
	f.seq++
	g := &Grant{Name: fmt.Sprintf("grant-%d", f.seq), State: StateAwaiting, Request: req, Requester: requester}
	f.grants[fkey(req)] = g
	return *g, nil
}

func (f *Fake) ListGrants(_ context.Context, class, target string) ([]Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Grant{}
	for _, g := range f.grants {
		if g.Request.Class == class && g.Request.Target == target {
			out = append(out, *g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *Fake) Revoke(_ context.Context, req Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.grants[fkey(req)]; ok && g.State.Open() {
		g.State = StateRevoked
	}
	return nil
}

// Approve flips the grant for req to ACTIVE (test-only: simulates an approver).
func (f *Fake) Approve(req Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.grants[fkey(req)]; ok {
		g.State = StateActive
	}
}
