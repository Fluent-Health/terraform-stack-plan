package server

import (
	"fmt"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

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
// error detail and a link to the build log. Returns "" when nothing failed.
func failuresSection(g events.Graph, logURL string) string {
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
		b.WriteString("\n</details>\n\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}
