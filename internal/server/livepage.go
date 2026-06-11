package server

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"strings"

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

// livePage renders the auto-refreshing execution page via the DaisyUI template.
// svg and panel are trusted server-generated HTML, injected un-escaped; repo,
// title and the report body are auto-escaped. The report is preformatted text
// (no markdown engine — the PR check run carries the rich report).
func (a *App) livePage(repo, environment, reportMarkdown, svg, panel string) string {
	title := "Terraform plan"
	if environment != "" {
		title += " — " + environment
	}
	var buf bytes.Buffer
	_ = a.tmpl.ExecuteTemplate(&buf, "live.gohtml", struct {
		Title, Repo, Report string
		SVG, Panel          template.HTML
	}{
		Title:  title,
		Repo:   repo,
		Report: reportMarkdown,
		SVG:    template.HTML(svg),
		Panel:  template.HTML(panel),
	})
	return buf.String()
}
