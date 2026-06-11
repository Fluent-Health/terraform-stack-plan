package server

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// approvalPanel renders a per-(class,target) table from the stored gate targets.
// Generic and provider-neutral: it shows state only — the deep link to a
// provider's approval console is added by the approval backend in a later
// increment. Returns "" when there are no targets.
func approvalPanel(targets []store.GateTarget) string {
	if len(targets) == 0 {
		return ""
	}
	var rows strings.Builder
	for _, t := range targets {
		var label string
		switch t.State {
		case "ACTIVE":
			label = "✅ Approved"
		case "AWAITING", "APPROVAL_AWAITED":
			label = "⏳ Waiting"
		case "blocked":
			label = "⚠️ Blocked"
		default:
			label = "❌ " + html.EscapeString(t.State)
		}
		fmt.Fprintf(&rows, "<tr><td><code>%s</code></td><td><code>%s</code></td><td>%s</td></tr>",
			html.EscapeString(t.Class), html.EscapeString(t.Target), label)
	}
	return `<div class="panel"><table>` +
		`<thead><tr><th>Class</th><th>Target</th><th>State</th></tr></thead>` +
		`<tbody>` + rows.String() + `</tbody></table></div>`
}

// liveView is the data the live template renders.
type liveView struct {
	Exec                      string
	Repo, Environment, Report string
	Phase                     events.Phase
	Stacks                    []events.StackState
	SVG, Panel                string
}

// livePage renders the auto-refreshing execution page via the DaisyUI template.
// SVG and Panel are trusted server-generated HTML (injected un-escaped); Repo,
// Title, Report, stack paths/statuses, and phase names are auto-escaped.
func (a *App) livePage(v liveView) string {
	title := "Terraform plan"
	if v.Environment != "" {
		title += " — " + v.Environment
	}
	depth := a.cfg.GroupDepth
	if depth == 0 {
		depth = 2
	}
	var buf bytes.Buffer
	_ = a.tmpl.ExecuteTemplate(&buf, "live.gohtml", struct {
		Title, Exec, Repo, Report string
		Timeline                  []phaseStep
		Groups                    []stackGroup
		SVG, Panel                template.HTML
	}{
		Title:    title,
		Exec:     v.Exec,
		Repo:     v.Repo,
		Report:   v.Report,
		Timeline: phaseTimeline(v.Phase),
		Groups:   groupStacksByKey(v.Stacks, depth, a.groupRE),
		SVG:      template.HTML(v.SVG),
		Panel:    template.HTML(v.Panel),
	})
	return buf.String()
}
