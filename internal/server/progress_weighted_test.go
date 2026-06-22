package server

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// planSet: warming(1) + linting(1) + initializing(2) + planning(6) = total 10.
func planSet() []config.PhaseWeight {
	return []config.PhaseWeight{
		{Phase: events.PhaseWarming, Weight: 1},
		{Phase: events.PhaseLinting, Weight: 1},
		{Phase: events.PhaseInitializing, Weight: 2},
		{Phase: events.PhasePlanning, Weight: 6},
	}
}

func approxPct(t *testing.T, phases []config.PhaseWeight, phase events.Phase, planned, initialized, total, wantPct int) {
	t.Helper()
	_, _, pct := progress(phases, phase, planned, initialized, total)
	if pct != wantPct {
		t.Fatalf("phase %s (planned=%d init=%d total=%d): pct=%d, want %d", phase, planned, initialized, total, pct, wantPct)
	}
}

func TestWeightedProgressBands(t *testing.T) {
	ps := planSet() // total weight 10
	// warming is a marker → its full 1/10 band once entered = 10%.
	approxPct(t, ps, events.PhaseWarming, 0, 0, 0, 10)
	// linting marker → (1+1)/10 = 20%.
	approxPct(t, ps, events.PhaseLinting, 0, 0, 0, 20)
	// initializing (ticking) half done: (2) + 2*0.5 = 3/10 = 30%.
	approxPct(t, ps, events.PhaseInitializing, 0, 5, 10, 30)
	// initializing fully: (2)+2 = 4/10 = 40%.
	approxPct(t, ps, events.PhaseInitializing, 0, 10, 10, 40)
	// planning half: (4) + 6*0.5 = 7/10 = 70%.
	approxPct(t, ps, events.PhasePlanning, 5, 10, 10, 70)
	// planning complete → 100%.
	approxPct(t, ps, events.PhasePlanning, 10, 10, 10, 100)
}

func TestWeightedProgressFallback(t *testing.T) {
	// Nil phase set → built-in (legacy) fractions: warming = 5%.
	if _, _, pct := progress(nil, events.PhaseWarming, 0, 0, 0); pct != 5 {
		t.Fatalf("legacy warming pct = %d, want 5", pct)
	}
	// Phase not in the configured set → fall back to legacy (verifying = 100%).
	if _, _, pct := progress(planSet(), events.PhaseVerifying, 0, 0, 0); pct != 100 {
		t.Fatalf("out-of-set verifying pct = %d, want legacy 100", pct)
	}
}

func TestPhaseLabels(t *testing.T) {
	if got := phaseLabel(events.PhaseLinting, 0, 0, 0); got != "linting modules…" {
		t.Fatalf("lint label = %q", got)
	}
	if got := phaseLabel(events.PhaseTesting, 0, 0, 0); got != "testing…" {
		t.Fatalf("test label = %q", got)
	}
}
