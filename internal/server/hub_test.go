package server

import (
	"sync"
	"testing"
	"time"
)

func TestHubPublishReachesSubscriber(t *testing.T) {
	h := newHub()
	ch, unsub := h.subscribe("e1|a")
	defer unsub()
	h.publish("e1|a", "hello")
	select {
	case got := <-ch:
		if got != "hello" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the chunk")
	}
}

func TestHubPublishToOtherKeyNotReceived(t *testing.T) {
	h := newHub()
	ch, unsub := h.subscribe("e1|a")
	defer unsub()
	h.publish("e1|b", "nope")
	select {
	case got := <-ch:
		t.Fatalf("received cross-key chunk %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubUnsubscribeStopsDelivery(t *testing.T) {
	h := newHub()
	ch, unsub := h.subscribe("e1|a")
	unsub()
	h.publish("e1|a", "after-unsub")
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("received after unsubscribe")
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubPublishNonBlockingOnFullChannel(t *testing.T) {
	h := newHub()
	_, unsub := h.subscribe("e1|a") // never drained
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100000; i++ {
			h.publish("e1|a", "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a full subscriber channel")
	}
}

func TestHubConcurrent(t *testing.T) {
	h := newHub()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, unsub := h.subscribe("k")
			h.publish("k", "v")
			select {
			case <-ch:
			case <-time.After(time.Second):
			}
			unsub()
		}()
	}
	wg.Wait()
}
