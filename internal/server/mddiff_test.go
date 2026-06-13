package server

import (
	"strings"
	"testing"
)

func TestRenderMarkdownColorizesDiffFences(t *testing.T) {
	md := "```diff\n+ added line\n- removed line\n~ changed line\ncontext line\n```\n"
	out := string(renderMarkdown(md))
	for _, want := range []string{
		`<pre class="tfsp-diff">`,
		`<span class="diff-add">+ added line`,
		`<span class="diff-del">- removed line`,
		`<span class="diff-chg">~ changed line`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "context line") {
		t.Errorf("context line lost:\n%s", out)
	}
}

func TestRenderMarkdownEscapesDiffContent(t *testing.T) {
	md := "```diff\n+ <script>alert(1)</script>\n```\n"
	out := string(renderMarkdown(md))
	if strings.Contains(out, "<script>") {
		t.Fatalf("unescaped HTML in diff output:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("escaped content missing:\n%s", out)
	}
}

func TestRenderMarkdownLeavesOtherFencesAlone(t *testing.T) {
	md := "```hcl\nresource \"x\" \"y\" {}\n```\n"
	out := string(renderMarkdown(md))
	if !strings.Contains(out, `<code class="language-hcl">`) {
		t.Fatalf("non-diff fence lost its default rendering:\n%s", out)
	}
	if strings.Contains(out, "tfsp-diff") {
		t.Fatalf("non-diff fence got diff classes:\n%s", out)
	}
}
