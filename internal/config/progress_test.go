package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func loadHCL(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), DefaultFilename)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestProgressBlock(t *testing.T) {
	cfg, err := loadHCL(t, `
progress {
  plan {
    phase "warming"      {}
    phase "linting"      {}
    phase "initializing" {}
    phase "planning"     { weight = 20 }
  }
  apply {
    phase "warming"      {}
    phase "initializing" {}
    phase "applying"     {}
    phase "verifying"    {}
  }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Progress == nil {
		t.Fatal("expected progress block")
	}
	plan := cfg.Progress.For("plan")
	if len(plan) != 4 {
		t.Fatalf("plan phases = %d, want 4", len(plan))
	}
	// order preserved
	wantOrder := []events.Phase{events.PhaseWarming, events.PhaseLinting, events.PhaseInitializing, events.PhasePlanning}
	for i, pw := range plan {
		if pw.Phase != wantOrder[i] {
			t.Fatalf("plan[%d] = %q, want %q", i, pw.Phase, wantOrder[i])
		}
	}
	// default weight applied to warming (1), explicit override on planning (20)
	if plan[0].Weight != 1 {
		t.Fatalf("warming default weight = %v, want 1", plan[0].Weight)
	}
	if plan[3].Weight != 20 {
		t.Fatalf("planning override weight = %v, want 20", plan[3].Weight)
	}
	if got := cfg.Progress.For("apply"); len(got) != 4 || got[2].Phase != events.PhaseApplying {
		t.Fatalf("apply phases unexpected: %+v", got)
	}
}

func TestProgressCustomPhases(t *testing.T) {
	cfg, err := loadHCL(t, `
progress {
  plan {
    phase "warming" {}
    phase "bogus"   {}
  }
}
`)
	if err != nil {
		t.Fatalf("expected custom phase to parse successfully, got error: %v", err)
	}
	got := cfg.Progress.For("plan")
	if len(got) != 2 || got[1].Phase != "bogus" {
		t.Fatalf("expected custom phase 'bogus' in plan phases, got: %+v", got)
	}
}

func TestProgressForNil(t *testing.T) {
	var pc *ProgressConfig
	if pc.For("plan") != nil {
		t.Fatal("nil ProgressConfig.For should be nil")
	}
}
