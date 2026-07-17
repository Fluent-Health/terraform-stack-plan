package server

// Markdown rendering + execution-kind helpers shared by the check-run surface
// and the /plan fragment endpoint (the central UI injects those fragments —
// the diff renderer has exactly one implementation, here).

import (
	"bytes"
	"html"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

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
