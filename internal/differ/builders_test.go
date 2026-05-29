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

func TestStructuralDiffOnlyChangedPaths(t *testing.T) {
	before := map[string]any{"spec": map[string]any{"replicas": 3.0, "image": "v1"}}
	after := map[string]any{"spec": map[string]any{"replicas": 5.0, "image": "v1"}}
	out := structuralDiff(before, after)
	if !strings.Contains(out, "spec.replicas: 3 -> 5") {
		t.Fatalf("expected changed replicas path, got:\n%s", out)
	}
	if strings.Contains(out, "image") {
		t.Fatalf("unchanged image should be omitted, got:\n%s", out)
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
