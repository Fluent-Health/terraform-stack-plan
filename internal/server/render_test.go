package server

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestFailuresSection(t *testing.T) {
	none := events.Graph{Stacks: []events.StackState{{Path: "a", Status: events.StatusPlanned}}}
	if failuresSection(none, "", "") != "" {
		t.Error("no failures should render empty")
	}
	g := events.Graph{Stacks: []events.StackState{{Path: "stacks/x", Status: events.StatusFailed, Detail: "boom"}}}
	out := failuresSection(g, "https://ci/log", "")
	for _, want := range []string{"Failures (1)", "stacks/x", "boom", "https://ci/log"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestFailuresSectionPerStackLogLink asserts that when a per-stack log prefix is
// given (apply context), each failing stack gets a deep-link to its own streamed
// log at <prefix>/<stack>, and the failure detail (init vs apply phase) renders.
func TestFailuresSectionPerStackLogLink(t *testing.T) {
	g := events.Graph{Stacks: []events.StackState{
		{Path: "cluster/fh-prod", Status: events.StatusFailed, Detail: "terraform apply failed"},
	}}
	out := failuresSection(g, "https://ci/log", "https://serve/logs/apply-1")
	for _, want := range []string{
		"cluster/fh-prod",
		"terraform apply failed",
		"https://serve/logs/apply-1/cluster/fh-prod",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestFailuresSectionTriage asserts that classifyFailure output is embedded in the
// failures section: IAM-denied stacks get a Likely cause + Next steps + State impact;
// unmatched errors get Next steps + State impact but NO fabricated Likely cause.
func TestFailuresSectionTriage(t *testing.T) {
	t.Run("iam_denied", func(t *testing.T) {
		g := events.Graph{Stacks: []events.StackState{
			{Path: "stacks/prod", Status: events.StatusFailed,
				Detail: "Error: Error 403: Permission 'run.services.setIamPolicy' denied"},
		}}
		out := failuresSection(g, "", "")
		for _, want := range []string{
			"Error 403",
			"Likely cause",
			"Next steps",
			"Re-request elevated access",
			"Safe to retry",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("IAM case: missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("unmatched_no_fabricated_cause", func(t *testing.T) {
		g := events.Graph{Stacks: []events.StackState{
			{Path: "stacks/dev", Status: events.StatusFailed,
				Detail: "panic: something totally novel"},
		}}
		out := failuresSection(g, "", "")
		if !strings.Contains(out, "panic: something totally novel") {
			t.Errorf("unmatched case: raw detail missing in:\n%s", out)
		}
		if strings.Contains(out, "Likely cause") {
			t.Errorf("unmatched case: fabricated 'Likely cause' must NOT appear in:\n%s", out)
		}
		if !strings.Contains(out, "Next steps") {
			t.Errorf("unmatched case: generic 'Next steps' should still render in:\n%s", out)
		}
	})

	t.Run("no_detail_suppresses_triage", func(t *testing.T) {
		// With no captured error, triage would advise "read the error" — which
		// contradicts the "_no detail_" note. Suppress it entirely in that case.
		g := events.Graph{Stacks: []events.StackState{
			{Path: "stacks/qa", Status: events.StatusFailed},
		}}
		out := failuresSection(g, "", "")
		if !strings.Contains(out, "No error detail captured") {
			t.Errorf("empty-detail case: should note no detail in:\n%s", out)
		}
		for _, unwanted := range []string{"Likely cause", "Next steps", "State impact"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("empty-detail case: %q must NOT render in:\n%s", unwanted, out)
			}
		}
	})
}

func TestPAMConsoleURLPointsAtApprovalTab(t *testing.T) {
	got := pamConsoleURL("fh-dev-svc")
	want := "https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=fh-dev-svc"
	if got != want {
		t.Fatalf("pamConsoleURL = %q, want %q", got, want)
	}
}

func TestCheckSummaryPlanFinalized(t *testing.T) {
	stacks := []events.StackState{
		{Path: "svc/a", Status: events.StatusPlanned, Counts: &events.Counts{Add: 6},
			Categories: []events.Category{{Name: "iam"}}},
		{Path: "svc/b", Status: events.StatusPlanned, Counts: &events.Counts{Destroy: 2},
			Categories: []events.Category{{Name: "destructive"}}},
		{Path: "svc/c", Status: events.StatusPlanned, Counts: &events.Counts{Change: 3}},
	}
	out := checkSummary("plan", "nonprod", events.PhasePlanning, stacks, "https://srv/live/e1")
	for _, want := range []string{
		"## Plan · nonprod",
		"+6", "~3", "−2", // op tally in the headline
		"(3 stacks)",
		"⚠️ destructive", "⚿ 1 IAM",
		"[live viewer ↗](https://srv/live/e1)",
		"| Stack | Ops | Risk | State |",
		"`svc/a`", "`svc/b`", "`svc/c`",
		"planned",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCheckSummaryPlanDegradesWhilePlanning(t *testing.T) {
	stacks := []events.StackState{
		{Path: "svc/a", Status: events.StatusPlanned}, // counts nil
		{Path: "svc/b", Status: events.StatusRunning},
	}
	out := checkSummary("plan", "nonprod", events.PhasePlanning, stacks, "https://srv/live/e1")
	if !strings.Contains(out, "planning 1/2") {
		t.Errorf("want degraded 'planning 1/2' headline in:\n%s", out)
	}
	if strings.Contains(out, "⚠️ destructive") {
		t.Errorf("no destructive chip when no counts/categories:\n%s", out)
	}
	if !strings.Contains(out, "`svc/a`") || !strings.Contains(out, "| Stack | Ops | Risk | State |") {
		t.Errorf("table should still render while planning:\n%s", out)
	}
}

func TestCheckSummaryApplyApplied(t *testing.T) {
	stacks := []events.StackState{
		{Path: "svc/a", Status: events.StatusSafe, Counts: &events.Counts{Add: 6}},
		{Path: "svc/b", Status: events.StatusSafe, Counts: &events.Counts{Destroy: 2}},
	}
	out := checkSummary("apply", "nonprod", events.PhaseApplying, stacks, "https://srv/live/e1")
	for _, want := range []string{"## Apply · nonprod", "applied 2/2", "+6", "−2", "applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCheckSummaryApplyPartialFailed(t *testing.T) {
	stacks := []events.StackState{
		{Path: "svc/a", Status: events.StatusSafe, Counts: &events.Counts{Add: 6}},
		{Path: "svc/b", Status: events.StatusFailed},
	}
	out := checkSummary("apply", "prod", events.PhaseApplying, stacks, "https://srv/live/e1")
	if !strings.Contains(out, "applied 1/2") {
		t.Errorf("want 'applied 1/2' in:\n%s", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("failed stack state must show in table:\n%s", out)
	}
}

func TestCheckSummaryNoEnvNoChips(t *testing.T) {
	stacks := []events.StackState{{Path: "svc/a", Status: events.StatusPlanned, Counts: &events.Counts{Change: 1}}}
	out := checkSummary("plan", "", events.PhasePlanning, stacks, "https://srv/live/e1")
	if strings.Contains(out, " · ") && strings.Contains(out, "## Plan ·") {
		t.Errorf("env empty → headline must not carry ' · <env>':\n%s", out)
	}
	if strings.Contains(out, "⚠️ destructive") || strings.Contains(out, "IAM") {
		t.Errorf("no risk chips when none present:\n%s", out)
	}
}

func TestCheckSummaryPlanningHeadlineHasBar(t *testing.T) {
	stacks := []events.StackState{
		{Path: "svc/a", Status: events.StatusPlanned}, // no Counts → in-progress branch
		{Path: "svc/b", Status: events.StatusRunning},
	}
	out := checkSummary("plan", "nonprod", events.PhasePlanning, stacks, "")
	for _, want := range []string{"## Plan · nonprod —", "▰", "▱", "planning 1/2"} {
		if !strings.Contains(out, want) {
			t.Errorf("planning headline missing %q in:\n%s", want, out)
		}
	}
}

func TestCheckSummaryWarmingNoStacks(t *testing.T) {
	out := checkSummary("plan", "nonprod", events.PhaseWarming, nil, "")
	if !strings.Contains(out, "warming cache…") {
		t.Errorf("warming headline missing label in:\n%s", out)
	}
	if strings.Contains(out, "| Stack | Ops | Risk | State |") {
		t.Errorf("empty graph must not render the per-stack table in:\n%s", out)
	}
}

func TestProgress(t *testing.T) {
	cases := []struct {
		name             string
		phase            events.Phase
		planned, total   int
		wantBar          string
		wantLabel        string
	}{
		{"warming", events.PhaseWarming, 0, 0, "▰▱▱▱▱▱▱▱▱▱", "warming cache…"},
		{"initializing", events.PhaseInitializing, 0, 12, "▰▰▱▱▱▱▱▱▱▱", "initializing 12 stacks…"},
		{"planning_mid", events.PhasePlanning, 5, 10, "▰▰▰▰▰▰▱▱▱▱", "planning 5/10"},
		{"planning_done", events.PhasePlanning, 10, 10, "▰▰▰▰▰▰▰▰▰▰", "planned"},
		{"planning_zero", events.PhasePlanning, 0, 0, "▰▰▱▱▱▱▱▱▱▱", "planning…"},
		{"applying_mid", events.PhaseApplying, 3, 6, "▰▰▰▰▰▱▱▱▱▱", "applying 3/6"},
		{"applying_done", events.PhaseApplying, 6, 6, "▰▰▰▰▰▰▰▰▰▰", "applied 6/6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bar, label, _ := progress(c.phase, c.planned, c.total)
			if bar != c.wantBar {
				t.Errorf("bar = %q, want %q", bar, c.wantBar)
			}
			if label != c.wantLabel {
				t.Errorf("label = %q, want %q", label, c.wantLabel)
			}
			if len([]rune(bar)) != progressCells {
				t.Errorf("bar width = %d, want %d", len([]rune(bar)), progressCells)
			}
		})
	}
}

func TestGatesSection(t *testing.T) {
	if gatesSection(nil) != "" {
		t.Error("no targets → no banner")
	}
	if s := gatesSection([]store.GateTarget{{Class: "iam", Target: "p", State: "ACTIVE"}}); s != "" {
		t.Errorf("all-active → no banner, got %q", s)
	}
	s := gatesSection([]store.GateTarget{
		{Class: "iam", Target: "fh-dev-svc", State: "AWAITING"},
		{Class: "iam", Target: "fh-stage-svc", State: "ACTIVE"},
	})
	if !strings.Contains(s, "Awaiting approval") || !strings.Contains(s, "fh-dev-svc") || !strings.Contains(s, pamConsoleURL("fh-dev-svc")) {
		t.Errorf("pending gate banner missing content: %q", s)
	}
	if strings.Contains(s, "fh-stage-svc") {
		t.Error("active gate should not appear in the awaiting banner")
	}
}

func TestErrorTail(t *testing.T) {
	t.Run("box_block", func(t *testing.T) {
		in := "Initializing...\nApplying...\n╷\n│ Error: creating Bucket: 403\n│\n│   on storage.tf line 8\n╵\n"
		got := errorTail(in, 25)
		if !strings.Contains(got, "Error: creating Bucket: 403") || !strings.Contains(got, "on storage.tf line 8") {
			t.Errorf("box block not extracted:\n%s", got)
		}
		if strings.Contains(got, "Initializing...") {
			t.Errorf("preamble should be dropped:\n%s", got)
		}
	})
	t.Run("error_line_no_box", func(t *testing.T) {
		in := "step 1 ok\nstep 2 ok\nError: terraform exited with code 1\n"
		got := errorTail(in, 25)
		if got != "Error: terraform exited with code 1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("fallback_last_lines", func(t *testing.T) {
		in := "a\nb\nc\nd\n"
		got := errorTail(in, 2)
		if got != "c\nd" {
			t.Errorf("fallback got %q, want \"c\\nd\"", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if errorTail("   \n\n", 25) != "" {
			t.Errorf("empty input must yield empty string")
		}
	})
	t.Run("cap", func(t *testing.T) {
		got := errorTail("l1\nl2\nl3\nl4\nl5\n", 3)
		if got != "l3\nl4\nl5" {
			t.Errorf("cap got %q", got)
		}
	})
	t.Run("open_box", func(t *testing.T) {
		// Box with no closing ╵ should return everything from ╷ to EOF (rule 1).
		in := "some preamble\n╷\n│ Error: something bad happened\n│ detail line\n"
		got := errorTail(in, 25)
		if !strings.Contains(got, "╷") || !strings.Contains(got, "Error: something bad happened") {
			t.Errorf("open_box: missing expected content in:\n%s", got)
		}
		if strings.Contains(got, "some preamble") {
			t.Errorf("open_box: preamble should be dropped in:\n%s", got)
		}
	})
	t.Run("box_block_long", func(t *testing.T) {
		// Rule 1 (box block) should return full content even when longer than maxLines.
		in := "╷\n│ line 1\n│ line 2\n│ line 3\n│ line 4\n│ line 5\n╵\n"
		got := errorTail(in, 3)
		// Box has 8 lines total (╷ + 5 content + ╵), should all be present.
		lines := strings.Split(got, "\n")
		// Count non-empty lines (or all lines for verification)
		if !strings.Contains(got, "│ line 1") || !strings.Contains(got, "│ line 5") {
			t.Errorf("box_block_long: not all box lines present (got %d lines) in:\n%s", len(lines), got)
		}
		// Verify it includes both opening and closing box characters
		if !strings.Contains(got, "╷") || !strings.Contains(got, "╵") {
			t.Errorf("box_block_long: missing box delimiters in:\n%s", got)
		}
	})
	t.Run("error_line_long", func(t *testing.T) {
		// Rule 2 (Error: to EOF) should return full content even when longer than maxLines.
		in := "some preamble\nError: first line\nline 2\nline 3\nline 4\nline 5\nline 6\n"
		got := errorTail(in, 3)
		// Should include all 6 lines from Error: to EOF, not capped.
		if !strings.Contains(got, "Error: first line") || !strings.Contains(got, "line 6") {
			t.Errorf("error_line_long: not all lines from Error: to EOF present in:\n%s", got)
		}
		if strings.Contains(got, "some preamble") {
			t.Errorf("error_line_long: preamble should be dropped in:\n%s", got)
		}
		// Verify we got all 6 lines (not truncated to maxLines=3)
		lines := strings.Split(got, "\n")
		if len(lines) != 6 {
			t.Errorf("error_line_long: got %d lines, want 6 (uncapped rule 2):\n%s", len(lines), got)
		}
	})
}
