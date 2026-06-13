package server

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"

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
		active := false
		switch t.State {
		case "ACTIVE":
			label, active = "✅ Approved", true
		case "AWAITING", "APPROVAL_AWAITED", "ACTIVATING", "SCHEDULED":
			label = "⏳ Awaiting approval"
		case "blocked":
			label = "⚠️ Blocked"
		default:
			label = "❌ " + html.EscapeString(t.State)
		}
		action := ""
		if !active {
			action = fmt.Sprintf(`<a class="approve-link" href="%s" target="_blank" rel="noopener">approve in PAM ↗</a>`,
				html.EscapeString(pamConsoleURL(t.Target)))
		}
		fmt.Fprintf(&rows, "<tr><td><code>%s</code></td><td><code>%s</code></td><td>%s</td><td>%s</td></tr>",
			html.EscapeString(t.Class), html.EscapeString(t.Target), label, action)
	}
	return `<div class="panel"><h2 class="text-sm font-semibold opacity-70 mb-2">Approvals</h2><table>` +
		`<thead><tr><th>Class</th><th>Target</th><th>State</th><th></th></tr></thead>` +
		`<tbody>` + rows.String() + `</tbody></table></div>`
}

// mdRenderer is a shared goldmark instance with GFM extensions and raw HTML
// passthrough (needed for <details>/<summary> blocks in plan reports).
var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(
		goldmarkhtml.WithUnsafe(),
		renderer.WithNodeRenderers(util.Prioritized(&diffCodeRenderer{}, 100)),
	),
)

// renderMarkdown converts GitHub-flavoured markdown to HTML. The output is
// returned as template.HTML (trusted: input comes from the render core and the
// CI repo, not from untrusted user input).
func renderMarkdown(md string) template.HTML {
	if md == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(md), &buf); err != nil {
		// Fallback: escape and wrap in <pre> so the raw text is still legible.
		return template.HTML("<pre>" + html.EscapeString(md) + "</pre>")
	}
	return template.HTML(buf.String())
}

// execKind returns "apply" when the execution Context begins with "apply",
// otherwise "plan".
func execKind(context string) string {
	if strings.HasPrefix(context, "apply") {
		return "apply"
	}
	return "plan"
}

// isFinished returns true when the execution should be treated as concluded:
// for a plan execution the report being present is the signal; for an apply
// execution a non-empty terminal Status is used.
func isFinished(kind, report, status string) bool {
	if kind == "apply" {
		switch status {
		case "success", "failure", "action_required", "cancelled", "timed_out":
			return true
		}
		return false
	}
	// plan: finished when the report has arrived
	return report != ""
}

// liveView is the data the live template renders.
type liveView struct {
	Exec                      string
	Repo, Environment, Report string
	PR                        int
	SHA, Context              string
	Phase                     events.Phase
	Status                    string
	Stacks                    []events.StackState
	SVG, Panel                string
}

// livePage renders the auto-refreshing execution page via the DaisyUI template.
// SVG and Panel are trusted server-generated HTML (injected un-escaped); Repo,
// Title, Report, stack paths/statuses, and phase names are auto-escaped.
func (a *App) livePage(v liveView) string {
	kind := execKind(v.Context)
	finished := isFinished(kind, v.Report, v.Status)

	// Build a human-readable kind + environment label for the title.
	kindLabel := "Plan"
	if kind == "apply" {
		kindLabel = "Apply"
	}
	title := kindLabel
	if v.Environment != "" {
		title += " · " + v.Environment
	}

	depth := a.cfg.GroupDepth
	if depth == 0 {
		depth = 2
	}
	// ShortSHA is the first 7 characters of the commit SHA, for display.
	shortSHA := v.SHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	var buf bytes.Buffer
	_ = a.tmpl.ExecuteTemplate(&buf, "live.gohtml", struct {
		Title, Exec, Repo, Report string
		PR                        int
		SHA, ShortSHA, Context    string
		ReportHTML                template.HTML
		Timeline                  []phaseStep
		Groups                    []stackGroup
		SVG, Panel                template.HTML
	}{
		Title:      title,
		Exec:       v.Exec,
		Repo:       v.Repo,
		Report:     v.Report,
		PR:         v.PR,
		SHA:        v.SHA,
		ShortSHA:   shortSHA,
		Context:    v.Context,
		ReportHTML: renderMarkdown(v.Report),
		Timeline:   phaseTimeline(kind, v.Phase, finished),
		Groups:     groupStacksByKey(v.Stacks, depth, a.groupRE),
		SVG:        template.HTML(v.SVG),
		Panel:      template.HTML(v.Panel),
	})
	return buf.String()
}
