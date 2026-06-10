package server

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestApprovalPanelStates(t *testing.T) {
	if approvalPanel(nil) != "" {
		t.Error("no targets → empty panel")
	}
	panel := approvalPanel([]store.GateTarget{
		{Class: "iam", Target: "proj-a", State: "ACTIVE"},
		{Class: "iam", Target: "proj-b", State: "AWAITING"},
		{Class: "database", Target: "proj-c", State: "blocked"},
	})
	for _, want := range []string{"proj-a", "proj-b", "proj-c", "iam", "database", "Approved", "Waiting", "Blocked"} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	if strings.Contains(panel, "console.cloud.google.com") || strings.Contains(panel, "cloud.google") {
		t.Error("approval panel must not hardcode a provider console URL")
	}
}

func TestLivePageEmbedsSVGReportAndPanel(t *testing.T) {
	g := sampleGraph()
	panel := approvalPanel([]store.GateTarget{{Class: "iam", Target: "proj-a", State: "AWAITING"}})
	page := livePage("owner/repo", "staging", "# the <report>", string(renderSVG(g)), panel)
	for _, want := range []string{
		"<!doctype html>", "owner/repo", "staging",
		"<svg ",
		"proj-a",
		"http-equiv=\"refresh\"",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("live page missing %q", want)
		}
	}
	if !strings.Contains(page, "# the &lt;report&gt;") {
		t.Errorf("report must be escaped in the page:\n%s", page)
	}
}

func TestLivePageEmptyReportShowsRunningNote(t *testing.T) {
	page := livePage("o/r", "staging", "", "<svg></svg>", "")
	if !strings.Contains(page, "still running") {
		t.Error("empty report should show a running placeholder")
	}
}
