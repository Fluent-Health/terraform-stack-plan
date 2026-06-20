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
	StackLogs                 map[string]string // stack path → recent log excerpt
	VerifyExec                string            // latest verify run id for this PR/env ("" if none)
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

// stackRow is one stack rendered in the grouped list AND its detail block below.
type stackRow struct {
	Path       string
	Anchor     string // same-page id the row scrolls to (path slug)
	State      stateDisplay
	Ops        string
	Risks      []riskTag
	PlanURL    string // /plan/{exec}/{path} — Result pane fetches this on open
	LogExcerpt string // recent log lines (shown when no diff yet)
	DetailURL  string // raw full log (text/plain)
	Follow     bool   // exec still running ⇒ stream the log via SSE follow
	Moved      bool   // state-only move: no plan diff, only log output
}

// anchorSlug turns a stack path into a safe same-page anchor id.
func anchorSlug(path string) string {
	var b strings.Builder
	b.WriteString("stack-")
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// projGroup is a Google-project group of stack rows.
type projGroup struct {
	Name   string
	Stacks []stackRow
}

// failureCard is one failed stack's triage information shown in the
// Needs-attention section at the top of the live execution page.
type failureCard struct {
	Path        string
	Class       string
	Cause       string
	Steps       []string
	StateImpact string
	Detail      string // raw error excerpt (shown in "What broke")
	LogURL      string // /logs/{exec}/{stack}
	Anchor      string // link to the stack's detail pane
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
	Destructive                               bool
	IAMCount                                  int
	Progress                                  []progSeg
	Groups                                    []projGroup
	Failures                                  []failureCard // non-empty when stacks have failed
	ReportHTML                                template.HTML
	Report                                    string // raw markdown — drives the "still running" vs report branch
	StackCount                                int
	Finished                                  bool   // concluded ⇒ static log fetch instead of SSE follow
	VerifyExec                                string // verify run id (apply only) for the Validation tab
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
	case finished:
		label = "PLANNED"
	case v.Phase == events.PhaseWarming:
		label = "WARMING"
	case v.Phase == events.PhaseInitializing:
		label = "INITIALIZING"
	case kind == "apply" && v.Phase == events.PhaseVerifying:
		label = "VERIFYING"
	case kind == "apply":
		label = "APPLYING"
	default:
		label = "PLANNING"
	}

	elapsed := ""
	if !v.CreatedAt.IsZero() {
		elapsed = humanizeDuration(now.Sub(v.CreatedAt))
	}

	// Risk roll-up + per-row mapping.
	var destructive bool
	groups := make([]projGroup, 0)
	for _, g := range groupByProject(v.Stacks) {
		rows := make([]stackRow, 0, len(g.Stacks))
		for _, s := range g.Stacks {
			risks := riskTags(s)
			for _, rt := range risks {
				if rt.CSS == "danger" {
					destructive = true
				}
			}
			logExcerpt := v.StackLogs[s.Path]
			rows = append(rows, stackRow{
				Path:       s.Path,
				Anchor:     anchorSlug(s.Path),
				State:      displayState(s.Status, kind),
				Ops:        opSummary(s.Counts),
				Risks:      risks,
				PlanURL:    "/plan/" + v.Exec + "/" + s.Path,
				LogExcerpt: logExcerpt,
				DetailURL:  "/logs/" + v.Exec + "/" + s.Path,
				Follow:     !finished,
				Moved:      s.Status == events.StatusMoving,
			})
		}
		groups = append(groups, projGroup{Name: g.Name, Stacks: rows})
	}

	// Collect failed stacks into triage cards for the Needs-attention section.
	// Triage is keyed off the captured error, so a failure with no detail gets no
	// card (it still shows red in the stack list) — mirroring failuresSection, and
	// avoiding "read the error" advice when there is no error to read.
	var failures []failureCard
	for _, s := range v.Stacks {
		if s.Status == events.StatusFailed && s.Detail != "" {
			tr := classifyFailure(s.Detail, s.Categories)
			failures = append(failures, failureCard{
				Path:        s.Path,
				Class:       tr.Class,
				Cause:       tr.Cause,
				Steps:       tr.Steps,
				StateImpact: tr.StateImpact,
				Detail:      s.Detail,
				LogURL:      "/logs/" + v.Exec + "/" + s.Path,
				Anchor:      anchorSlug(s.Path),
			})
		}
	}

	return liveModel{
		Title: title, Repo: v.Repo, Exec: v.Exec, SHA: v.SHA, ShortSHA: shortSHA,
		Context: v.Context, Environment: v.Environment, PR: v.PR,
		PhaseAccent: kind, PhaseLabel: label, Elapsed: elapsed,
		Verdict:     aggregateVerdict(v.Stacks),
		Destructive: destructive, IAMCount: iamCount(v.Stacks),
		Progress:   progressSegments(v.Stacks, kind),
		Groups:     groups,
		Failures:   failures,
		ReportHTML: renderMarkdown(v.Report),
		Report:     v.Report,
		StackCount: len(v.Stacks),
		Finished:   finished,
		VerifyExec: v.VerifyExec,
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
