package main

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestClassifyStep(t *testing.T) {
	const noChange = "...\nApply complete! Resources: 0 added, 0 changed, 0 destroyed.\n"
	const changed = "...\nApply complete! Resources: 0 added, 20 changed, 0 destroyed.\n"
	const planOut = "...\nPlan: 0 to add, 2 to change, 0 to destroy.\nSaved the plan to: plan.bin\n"

	cases := []struct {
		name      string
		exit      int
		output    string
		onSuccess events.Status
		want      events.Status
	}{
		{"apply no changes", 0, noChange, events.StatusSafe, events.StatusNochange},
		{"apply with changes", 0, changed, events.StatusSafe, events.StatusSafe},
		{"plan success not split", 0, planOut, events.StatusPlanned, events.StatusPlanned},
		{"failure", 1, changed, events.StatusSafe, events.StatusFailed},
		{"intermediate success (init)", 0, "Terraform has been successfully initialized!", "", events.Status("")},
		{"apply summary without onSuccess defaults safe", 0, changed, "", events.StatusSafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyStep(c.exit, c.output, c.onSuccess); got != c.want {
				t.Errorf("classifyStep(%d, …, %q) = %q, want %q", c.exit, c.onSuccess, got, c.want)
			}
		})
	}
}

func TestLogStreamer(t *testing.T) {
	var got []string
	s := newLogStreamer(func(chunk string) { got = append(got, chunk) })

	// One small write: buffered, not yet flushed.
	if _, err := s.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("small write flushed early: %v", got)
	}
	// Close flushes the remainder.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "hello " {
		t.Fatalf("after close got %v, want [\"hello \"]", got)
	}
}

func TestLogStreamerThresholdFlush(t *testing.T) {
	var got []string
	s := newLogStreamer(func(chunk string) { got = append(got, chunk) })
	big := make([]byte, 5000) // > 4KB threshold
	for i := range big {
		big[i] = 'x'
	}
	if _, err := s.Write(big); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("threshold write did not flush")
	}
	_ = s.Close()
	var total int
	for _, c := range got {
		total += len(c)
	}
	if total != 5000 {
		t.Fatalf("streamed %d bytes, want 5000", total)
	}
}
