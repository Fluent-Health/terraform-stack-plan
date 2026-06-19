package server

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// pamConsoleURL deep-links to the GCP PAM "Approve grants → Pending approval"
// tab for a target. The iam class emits the target as a GCP project
// (emit_attributes ["project"]), so the approver lands on that project's
// Privileged Access Manager approvals tab, where the pending grant is approved.
// GCP PAM exposes no per-grant deep link (only this project-scoped tab), so the
// link stops at the project. (gcp-pam is the only backend today; if more are
// added this should move behind the approval.Backend.)
func pamConsoleURL(target string) string {
	return "https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=" + url.QueryEscape(target)
}

// gatesSection renders an "awaiting approval" banner for the check run when any
// gate target is not yet ACTIVE, with a deep link to approve each in PAM. Returns
// "" when every gate is approved (or there are none).
func gatesSection(targets []store.GateTarget) string {
	var pending []store.GateTarget
	for _, t := range targets {
		if t.State != "ACTIVE" {
			pending = append(pending, t)
		}
	}
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### ⏳ Awaiting approval\n\nThis plan changes gated resources. Merge is blocked until each grant is approved in PAM:\n\n")
	for _, t := range pending {
		fmt.Fprintf(&b, "- 🔐 `%s` · `%s` — [approve in PAM ↗](%s)\n", t.Class, t.Target, pamConsoleURL(t.Target))
	}
	b.WriteString("\n---\n\n")
	return b.String()
}

// statusGlyph maps a stack status to a display glyph for the progress list.
func statusGlyph(s events.Status) string {
	switch s {
	case events.StatusPlanned, events.StatusSafe:
		return "✅"
	case events.StatusRunning:
		return "🔄"
	case events.StatusGated:
		return "🔐"
	case events.StatusMoving:
		return "🚚"
	case events.StatusFailed:
		return "❌"
	default:
		return "⏳"
	}
}

// done reports whether a stack has finished its work for progress accounting.
func done(s events.Status) bool {
	switch s {
	case events.StatusPlanned, events.StatusSafe, events.StatusMoving, events.StatusGated, events.StatusFailed:
		return true
	default:
		return false
	}
}

// renderProgress renders a minimal GFM task list of the execution's stacks with
// a done/total count. The render/UI sub-plan replaces this with the grouped list
// and the embedded DAG; the contract (a GFM string for the check-run summary)
// stays the same.
func renderProgress(g events.Graph) string {
	total := len(g.Stacks)
	doneCount := 0
	for _, s := range g.Stacks {
		if done(s.Status) {
			doneCount++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Plan progress (%d/%d)\n\n", doneCount, total)
	for _, s := range g.Stacks {
		box := " "
		if done(s.Status) {
			box = "x"
		}
		status := s.Status
		if status == "" {
			status = events.StatusPending
		}
		fmt.Fprintf(&b, "- [%s] %s `%s` — %s\n", box, statusGlyph(s.Status), s.Path, status)
	}
	return b.String()
}

// opTally formats the non-zero mutating-op kinds of a verdict as a compact
// blast-radius string: "+A ~C ±R −D ↔M" (only the kinds present). "" when none.
func opTally(v verdict) string {
	var parts []string
	if v.Add > 0 {
		parts = append(parts, fmt.Sprintf("+%d", v.Add))
	}
	if v.Change > 0 {
		parts = append(parts, fmt.Sprintf("~%d", v.Change))
	}
	if v.Replace > 0 {
		parts = append(parts, fmt.Sprintf("±%d", v.Replace))
	}
	if v.Destroy > 0 {
		parts = append(parts, fmt.Sprintf("−%d", v.Destroy))
	}
	if v.Move > 0 {
		parts = append(parts, fmt.Sprintf("↔%d", v.Move))
	}
	return strings.Join(parts, " ")
}

// checkSummary builds the check-run summary block for a plan or apply: a
// blast-radius headline, verdict chips (destructive / IAM) + a live-viewer link,
// and a per-stack table (Stack | Ops | Risk | State). kind is "plan" or "apply".
// While planning (no stack has counts yet) the headline degrades to a
// "planning d/t" progress count; the table still renders.
func checkSummary(kind, environment string, stacks []events.StackState, viewerURL string) string {
	v := aggregateVerdict(stacks)
	tally := opTally(v)

	hasCounts := false
	for _, s := range stacks {
		if s.Counts != nil {
			hasCounts = true
			break
		}
	}

	var b strings.Builder

	// Headline.
	heading := "Plan"
	if kind == "apply" {
		heading = "Apply"
	}
	b.WriteString("## " + heading)
	if environment != "" {
		b.WriteString(" · " + environment)
	}
	switch {
	case kind == "apply":
		applied := 0
		for _, s := range stacks {
			if s.Status == events.StatusSafe {
				applied++
			}
		}
		fmt.Fprintf(&b, " — applied %d/%d", applied, len(stacks))
		if tally != "" {
			b.WriteString(" · " + tally)
		}
	case !hasCounts:
		doneCount := 0
		for _, s := range stacks {
			if done(s.Status) {
				doneCount++
			}
		}
		fmt.Fprintf(&b, " — planning %d/%d", doneCount, len(stacks))
	default:
		if tally != "" {
			b.WriteString(" — " + tally)
		}
		fmt.Fprintf(&b, "  (%d stacks)", len(stacks))
	}
	b.WriteString("\n")

	// Verdict chips + viewer link.
	var chips []string
	destructive := false
	for _, s := range stacks {
		for _, rt := range riskTags(s) {
			if rt.CSS == "danger" {
				destructive = true
			}
		}
	}
	if destructive {
		chips = append(chips, "⚠️ destructive")
	}
	if n := iamCount(stacks); n > 0 {
		chips = append(chips, fmt.Sprintf("⚿ %d IAM", n))
	}
	if viewerURL != "" {
		chips = append(chips, fmt.Sprintf("[live viewer ↗](%s)", viewerURL))
	}
	if len(chips) > 0 {
		b.WriteString(strings.Join(chips, " · ") + "\n")
	}
	b.WriteString("\n")

	// Per-stack table.
	b.WriteString("| Stack | Ops | Risk | State |\n|---|---|---|---|\n")
	for _, s := range stacks {
		var risks []string
		for _, rt := range riskTags(s) {
			risks = append(risks, rt.Label)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n",
			s.Path, opSummary(s.Counts), strings.Join(risks, " "), displayState(s.Status, kind).Label)
	}
	return b.String()
}

// failuresSection renders a collapsible block per failed stack with the captured
// error detail (which, for an apply, names the phase it died in — terraform init
// vs apply) and links. logURL is the CI build log; stackLogPrefix, when non-empty
// (apply context), is the per-execution log base (e.g. "<base>/logs/<exec>") used
// to deep-link each failing stack's own streamed log at "<prefix>/<stack>".
// Returns "" when nothing failed.
func failuresSection(g events.Graph, logURL, stackLogPrefix string) string {
	var failed []events.StackState
	for _, s := range g.Stacks {
		if s.Status == events.StatusFailed {
			failed = append(failed, s)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### ❌ Failures (%d)\n\n", len(failed))
	if logURL != "" {
		fmt.Fprintf(&b, "🔗 [Full build log](%s)\n\n", logURL)
	}
	for _, s := range failed {
		fmt.Fprintf(&b, "<details><summary><code>%s</code></summary>\n\n", s.Path)
		if s.Detail != "" {
			fmt.Fprintf(&b, "```\n%s\n```\n", s.Detail)
			// Triage is keyed off the captured error, so only emit it when there
			// is one — with no detail the "_see the build log_" note below is the
			// only honest advice (the generic steps would say "read the error").
			tr := classifyFailure(s.Detail, s.Categories)
			if tr.Cause != "" {
				fmt.Fprintf(&b, "\n**Likely cause.** %s\n", tr.Cause)
			}
			if len(tr.Steps) > 0 {
				b.WriteString("\n**Next steps:**\n\n")
				for i, step := range tr.Steps {
					fmt.Fprintf(&b, "%d. %s\n", i+1, step)
				}
			}
			if tr.StateImpact != "" {
				fmt.Fprintf(&b, "\n**State impact.** %s\n", tr.StateImpact)
			}
		} else {
			b.WriteString("_No error detail captured — see the build log._\n")
		}
		if stackLogPrefix != "" {
			fmt.Fprintf(&b, "\n📄 [Stack log](%s/%s)\n", strings.TrimRight(stackLogPrefix, "/"), s.Path)
		}
		b.WriteString("\n</details>\n\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}
