package server

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// diffCodeRenderer overrides fenced-code rendering to colorize ```diff blocks
// GitHub-style (per-line add/del/change spans). Non-diff fences reproduce
// goldmark's default output so registering this renderer changes nothing else.
type diffCodeRenderer struct{}

func (r *diffCodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, r.render)
}

func (r *diffCodeRenderer) render(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.FencedCodeBlock)
	if string(n.Language(source)) == "diff" {
		return r.renderDiff(w, source, n, entering)
	}
	return r.renderDefault(w, source, n, entering)
}

// renderDiff emits <pre class="tfsp-diff"><code> with one classed span per
// +/-/~/! line; other lines pass through escaped.
func (r *diffCodeRenderer) renderDiff(w util.BufWriter, source []byte, n *ast.FencedCodeBlock, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_, _ = w.WriteString("</code></pre>\n")
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<pre class="tfsp-diff"><code>`)
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		raw := line.Value(source)
		// goldmark includes the trailing newline in the line value. Emit it
		// OUTSIDE the span so white-space:pre breaks between lines — otherwise the
		// per-line inline-block spans have no break point between them and run
		// together on one horizontally-scrolling line.
		nl := ""
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			raw, nl = raw[:len(raw)-1], "\n"
		}
		cls := ""
		if len(raw) > 0 {
			switch raw[0] {
			case '+':
				cls = "diff-add"
			case '-':
				cls = "diff-del"
			case '~', '!':
				cls = "diff-chg"
			}
		}
		if cls != "" {
			_, _ = w.WriteString(`<span class="` + cls + `">`)
		}
		_, _ = w.Write(util.EscapeHTML(raw))
		if cls != "" {
			_, _ = w.WriteString("</span>")
		}
		_, _ = w.WriteString(nl)
	}
	return ast.WalkContinue, nil
}

// renderDefault reproduces goldmark's stock fenced-code output:
// <pre><code class="language-X">…escaped…</code></pre>
func (r *diffCodeRenderer) renderDefault(w util.BufWriter, source []byte, n *ast.FencedCodeBlock, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_, _ = w.WriteString("</code></pre>\n")
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("<pre><code")
	if lang := n.Language(source); lang != nil {
		_, _ = w.WriteString(` class="language-`)
		_, _ = w.Write(util.EscapeHTML(lang))
		_, _ = w.WriteString(`"`)
	}
	_ = w.WriteByte('>')
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		_, _ = w.Write(util.EscapeHTML(line.Value(source)))
	}
	return ast.WalkContinue, nil
}
