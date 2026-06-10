package server

import (
	"fmt"
	"html"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// liveCSS styles the live page with a neutral palette (GitHub-ish light tones).
const liveCSS = `body{margin:0;background:#f6f8fa;color:#1f2328;font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;line-height:1.5}
.wrap{max-width:1280px;margin:0 auto;padding:24px}
h1{font-size:20px;margin:0 0 4px}
.sub{color:#656d76;font-size:13px;margin:0 0 20px}
.card{background:#fff;border:1px solid #d0d7de;border-radius:12px;padding:12px;margin-bottom:20px}
.card svg{max-width:100%;height:auto;display:block;margin:0 auto}
.panel{background:#fff;border:1px solid #d0d7de;border-radius:12px;padding:12px;margin-bottom:20px}
.panel table{border-collapse:collapse;width:100%;font-size:14px}
.panel th,.panel td{border:1px solid #d0d7de;padding:8px 12px;text-align:left}
.panel th{background:#f6f8fa;font-weight:600}
.panel code{background:#f6f8fa;padding:1px 5px;border-radius:4px;font-family:ui-monospace,Menlo,monospace;font-size:12px}
.report{background:#fff;border:1px solid #d0d7de;border-radius:12px;padding:20px 24px}
.report pre{background:#0d1117;color:#e6edf3;padding:12px;border-radius:8px;overflow:auto;font-size:12px}
.muted{color:#656d76;font-style:italic}`

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

// livePage renders the auto-refreshing execution page: the embedded SVG diagram,
// the approval panel, and the plan report. The report is shown as escaped
// preformatted text (no markdown engine — the rich rendering is a later phase's
// job; the PR check run carries the formatted report). svg and panel are trusted
// server-generated HTML and embedded as-is.
func livePage(repo, environment, reportMarkdown, svg, panel string) string {
	title := "Terraform plan"
	if environment != "" {
		title += " — " + environment
	}
	report := `<p class="muted">Plan still running — this page refreshes every 10s; the report appears here when the plan completes.</p>`
	if strings.TrimSpace(reportMarkdown) != "" {
		report = "<pre>" + html.EscapeString(reportMarkdown) + "</pre>"
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">`+
		`<meta http-equiv="refresh" content="10"><title>%s</title><style>%s</style></head>`+
		`<body><div class="wrap"><h1>%s</h1><p class="sub">%s · live — refreshes every 10s</p>`+
		`%s<div class="card">%s</div><div class="report">%s</div></div></body></html>`,
		html.EscapeString(title), liveCSS, html.EscapeString(title), html.EscapeString(repo),
		panel, svg, report)
}
