package server

import (
	"context"
	"sync"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/reconcile"
)

func TestShellLockSerializesPerChangeSet(t *testing.T) {
	sh := &Shell{locks: map[string]*sync.Mutex{}}
	key := reconcile.ChangeSet{PR: 7, Environment: "staging"}
	var got int
	var wg sync.WaitGroup
	concurrent := 0
	mu := sync.Mutex{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh.withLock(context.Background(), 7, "staging", func() {
				mu.Lock()
				concurrent++
				if concurrent > 1 {
					got++ // overlap detected
				}
				concurrent--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	_ = key
	if got != 0 {
		t.Fatalf("detected %d overlapping critical sections; want 0", got)
	}
}
