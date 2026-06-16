package server

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// pamConsoleURL deep-links to the GCP PAM grants page for a target. The iam
// class emits the target as a GCP project (emit_attributes ["project"]), so the
// approver lands on that project's Privileged Access Manager → Grants, where the
// pending grant is approved. (gcp-pam is the only backend today; if more are
// added this should move behind the approval.Backend.)
func pamConsoleURL(target string) string {
	return "https://console.cloud.google.com/iam-admin/pam/grants?project=" + url.QueryEscape(target)
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
