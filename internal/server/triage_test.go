package server

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestClassifyFailure(t *testing.T) {
	iam := []events.Category{{Name: "iam"}}
	cases := []struct {
		name      string
		detail    string
		cats      []events.Category
		wantClass string
		causeHas  string // substring (lowercased) expected in Cause ("" = expect empty Cause)
		retry     bool
	}{
		{"iam denied", "Error: Error 403: Permission 'run.services.setIamPolicy' denied", iam, "iam_denied", "permission", true},
		{"state lock", "Error: Error acquiring the state lock\nLock Info:\n  ID: 1234", nil, "state_lock", "lock", true},
		{"quota", "Error 403: Quota exceeded for quota metric 'CPUs'", nil, "quota", "quota", false},
		{"already exists", "Error 409: ... alreadyExists", nil, "already_exists", "already exists", false},
		{"move failed", "cross-state move failed: dest push failed; source rolled back from .tfsp-state-backups", nil, "state_move", "state move", true},
		{"unknown raw", "panic: something totally novel", nil, "error", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := classifyFailure(c.detail, c.cats)
			if tr.Class != c.wantClass {
				t.Fatalf("Class=%q want %q", tr.Class, c.wantClass)
			}
			if c.causeHas != "" && !strings.Contains(strings.ToLower(tr.Cause), c.causeHas) {
				t.Fatalf("Cause=%q want substr %q", tr.Cause, c.causeHas)
			}
			if c.causeHas == "" && tr.Cause != "" {
				t.Fatalf("unknown error must have empty Cause, got %q", tr.Cause)
			}
			if tr.Retryable != c.retry {
				t.Fatalf("Retryable=%v want %v", tr.Retryable, c.retry)
			}
			if len(tr.Steps) == 0 {
				t.Fatal("expected next steps")
			}
		})
	}
}

func TestClassifyFailureStateImpact(t *testing.T) {
	tr := classifyFailure("cross-state move failed: ... .tfsp-state-backups/...", nil)
	if !strings.Contains(strings.ToLower(tr.StateImpact), "restore") && !strings.Contains(tr.StateImpact, ".tfsp-state-backups") {
		t.Fatalf("move failure should surface state impact, got %q", tr.StateImpact)
	}
}
