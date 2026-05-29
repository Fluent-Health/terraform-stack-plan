package differ

import (
	"strings"
	"testing"
)

func TestDetectType(t *testing.T) {
	if got := detect(`{"a":1}`); got != typeJSON {
		t.Errorf("json detect = %v", got)
	}
	if got := detect("a: 1\nb: 2\n"); got != typeYAML {
		t.Errorf("yaml detect = %v", got)
	}
	if got := detect("just some text"); got != typePlain {
		t.Errorf("plain detect = %v", got)
	}
	long := strings.Repeat("QUJD", 60) // valid base64 charset, long
	if got := detect(long); got != typeBase64 {
		t.Errorf("base64 detect = %v, want base64", got)
	}
}

func TestContextDiff(t *testing.T) {
	before := "a: 1\nb: 2\nc: 3\nd: 4\n"
	after := "a: 1\nb: 2\nc: 9\nd: 4\n"
	out := contextDiff(before, after)
	if !strings.Contains(out, "-c: 3") || !strings.Contains(out, "+c: 9") {
		t.Fatalf("expected -/+ for the changed line, got:\n%s", out)
	}
	if !strings.Contains(out, " b: 2") || !strings.Contains(out, " d: 4") {
		t.Fatalf("expected 2 lines of context around the change, got:\n%s", out)
	}
}

func TestContextDiffMultiHunkSeparator(t *testing.T) {
	before := "a: 0\n" + strings.Repeat("x: 1\n", 10) + "z: 0\n"
	after := "a: 9\n" + strings.Repeat("x: 1\n", 10) + "z: 9\n"
	out := contextDiff(before, after)
	if !strings.Contains(out, "⋮") {
		t.Fatalf("expected ⋮ between non-adjacent hunks, got:\n%s", out)
	}
}

func TestLineDiff(t *testing.T) {
	out := lineDiff("line1\nline2\n", "line1\nCHANGED\n")
	if !strings.Contains(out, "-line2") || !strings.Contains(out, "+CHANGED") {
		t.Fatalf("expected -/+ lines, got:\n%s", out)
	}
}

func TestSummaryLine(t *testing.T) {
	out := summaryLine("content", "yaml", 412, 18)
	if !strings.Contains(out, "412 lines") || !strings.Contains(out, "18 changed") {
		t.Fatalf("bad summary: %s", out)
	}
}
