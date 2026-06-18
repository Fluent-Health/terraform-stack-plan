package server

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"strings"
	"time"

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
	CreatedAt                 time.Time
	Stacks                    []events.StackState
	SVG, Panel                string
}

// livePage renders the auto-refreshing execution page via the Briefing template.
// SVG and Panel are trusted server-generated HTML (injected un-escaped); repo,
// title, report, stack paths/states, and phase labels are auto-escaped.
func (a *App) livePage(v liveView) string {
	kind := execKind(v.Context)
	finished := isFinished(kind, v.Report, v.Status)
	m := buildLiveModel(v, kind, finished, time.Now())
	var buf bytes.Buffer
	_ = a.tmpl.ExecuteTemplate(&buf, "live.gohtml", m)
	return buf.String()
}

// stackRow is one stack rendered in the grouped list.
type stackRow struct {
	Path  string
	State stateDisplay
	Ops   string
	Risks []riskTag
}

// projGroup is a Google-project group of stack rows.
type projGroup struct {
	Name   string
	Stacks []stackRow
}

// liveModel is the typed payload the Briefing live template renders.
type liveModel struct {
	Title, Repo, Exec, SHA, ShortSHA, Context string
	Environment                               string
	PR                                        int
	PhaseAccent                               string // "plan" | "apply" → phase-<accent>
	PhaseLabel                                string // PLANNING | PLANNED | APPLYING | APPLIED | FAILED
	Elapsed                                   string
	Verdict                                   verdict
	Destructive, IAM                          bool
	Blast                                     []blastSeg
	Groups                                    []projGroup
	ReportHTML                                template.HTML
	Report                                    string // raw markdown — drives the "still running" vs report branch
	StackCount                                int
	SVG, Panel                                template.HTML
}

// buildLiveModel assembles the Briefing payload from a liveView. kind is
// "plan"/"apply"; finished marks a concluded execution; now is the reference
// clock for elapsed (injected for testability).
func buildLiveModel(v liveView, kind string, finished bool, now time.Time) liveModel {
	shortSHA := v.SHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}
	kindLabel := "Plan"
	if kind == "apply" {
		kindLabel = "Apply"
	}
	title := kindLabel
	if v.Environment != "" {
		title += " · " + v.Environment
	}

	// Phase label.
	var label string
	switch {
	case kind == "apply" && finished && v.Status == "failure":
		label = "FAILED"
	case kind == "apply" && finished:
		label = "APPLIED"
	case kind == "apply":
		label = "APPLYING"
	case finished:
		label = "PLANNED"
	default:
		label = "PLANNING"
	}

	elapsed := ""
	if !v.CreatedAt.IsZero() {
		elapsed = humanizeDuration(now.Sub(v.CreatedAt))
	}

	// Risk roll-up + per-row mapping.
	var destructive, iam bool
	groups := make([]projGroup, 0)
	for _, g := range groupByProject(v.Stacks) {
		rows := make([]stackRow, 0, len(g.Stacks))
		for _, s := range g.Stacks {
			risks := riskTags(s)
			for _, rt := range risks {
				switch rt.CSS {
				case "danger":
					destructive = true
				case "iam":
					iam = true
				}
			}
			rows = append(rows, stackRow{
				Path:  s.Path,
				State: displayState(s.Status, kind),
				Ops:   opSummary(s.Counts),
				Risks: risks,
			})
		}
		groups = append(groups, projGroup{Name: g.Name, Stacks: rows})
	}

	return liveModel{
		Title: title, Repo: v.Repo, Exec: v.Exec, SHA: v.SHA, ShortSHA: shortSHA,
		Context: v.Context, Environment: v.Environment, PR: v.PR,
		PhaseAccent: kind, PhaseLabel: label, Elapsed: elapsed,
		Verdict:     aggregateVerdict(v.Stacks),
		Destructive: destructive, IAM: iam,
		Blast:      blastSegments(v.Stacks, kind),
		Groups:     groups,
		ReportHTML: renderMarkdown(v.Report),
		Report:     v.Report,
		StackCount: len(v.Stacks),
		SVG:        template.HTML(v.SVG),
		Panel:      template.HTML(v.Panel),
	}
}

// humanizeDuration renders a short elapsed string: "Xh Ym", "Xm Ys", or "Xs".
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
