package differ

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

func levels(ad model.AttrDiff) []model.Level {
	var out []model.Level
	for _, v := range ad.Variants {
		out = append(out, v.Level)
	}
	return out
}

func TestScalarInline(t *testing.T) {
	ad := Diff(Input{Attr: "role", Before: "roles/viewer", After: "roles/editor"})
	if len(ad.Variants) != 1 || ad.Variants[0].Level != model.LevelInline {
		t.Fatalf("scalar should be a single inline variant, got %v", levels(ad))
	}
}

func TestSensitiveInline(t *testing.T) {
	ad := Diff(Input{Attr: "password", Before: "a", After: "b", Sensitive: true})
	if ad.Variants[0].Content == "" || ad.Variants[0].Level != model.LevelInline {
		t.Fatalf("sensitive should render one inline (sensitive value) variant")
	}
}

func TestUnknownInline(t *testing.T) {
	ad := Diff(Input{Attr: "id", Unknown: true})
	if len(ad.Variants) != 1 {
		t.Fatalf("unknown should be a single inline variant")
	}
}

func TestStructuredYAMLLadder(t *testing.T) {
	before := "spec:\n  replicas: 3\n  image: v1\n"
	after := "spec:\n  replicas: 5\n  image: v1\n"
	ad := Diff(Input{Attr: "manifest", Before: before, After: after})
	want := []model.Level{model.LevelStructural, model.LevelSummary, model.LevelHidden}
	if got := levels(ad); !equalLevels(got, want) {
		t.Fatalf("yaml ladder = %v, want %v", got, want)
	}
}

func TestForcedDifferLine(t *testing.T) {
	before := "a: 1\n"
	after := "a: 2\n"
	ad := Diff(Input{Attr: "manifest", Before: before, After: after, ForceDiffer: "line"})
	if levels(ad)[0] != model.LevelLineDiff {
		t.Fatalf("forced line differ should start with LineDiff, got %v", levels(ad))
	}
}

func TestMaxAttributeLinesCeiling(t *testing.T) {
	before := "spec:\n  a: 1\n  b: 2\n  c: 3\n"
	after := "spec:\n  a: 9\n  b: 8\n  c: 7\n"
	ad := Diff(Input{Attr: "manifest", Before: before, After: after, MaxLines: 1})
	if levels(ad)[0] != model.LevelSummary {
		t.Fatalf("tiny cap should start at Summary, got %v", levels(ad))
	}
}

func equalLevels(a, b []model.Level) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNativeMapStructuralSummaryHasCounts(t *testing.T) {
	before := map[string]any{"replicas": 3.0, "image": "v1"}
	after := map[string]any{"replicas": 5.0, "image": "v2"}
	ad := Diff(Input{Attr: "settings", Before: before, After: after})
	// ladder should be [Structural, Summary, Hidden]
	if len(ad.Variants) != 3 {
		t.Fatalf("want 3 variants, got %d (%v)", len(ad.Variants), levels(ad))
	}
	sum := ad.Variants[1]
	if sum.Level != model.LevelSummary {
		t.Fatalf("variant[1] should be Summary, got %v", sum.Level)
	}
	if strings.Contains(sum.Content, "0 lines") || strings.Contains(sum.Content, "0 changed") {
		t.Fatalf("native-map summary should not report zero counts: %q", sum.Content)
	}
}

func TestBase64Ladder(t *testing.T) {
	before := strings.Repeat("QUJD", 60)
	after := strings.Repeat("WFla", 60)
	ad := Diff(Input{Attr: "data", Before: before, After: after})
	want := []model.Level{model.LevelSummary, model.LevelHidden}
	if got := levels(ad); !equalLevels(got, want) {
		t.Fatalf("base64 ladder = %v, want %v", got, want)
	}
	if !strings.Contains(ad.Variants[0].Content, "base64") {
		t.Fatalf("base64 summary should mention base64: %q", ad.Variants[0].Content)
	}
}

func TestNoDetectForcesLine(t *testing.T) {
	before := "spec:\n  replicas: 3\n  image: v1\n"
	after := "spec:\n  replicas: 5\n  image: v1\n"
	// Without NoDetect this YAML string would start Structural; with NoDetect it must start LineDiff.
	ad := Diff(Input{Attr: "manifest", Before: before, After: after, NoDetect: true})
	if levels(ad)[0] != model.LevelLineDiff {
		t.Fatalf("NoDetect should force LineDiff, got %v", levels(ad))
	}
}

func TestForcedSummary(t *testing.T) {
	ad := Diff(Input{Attr: "x", Before: "a\nb\nc\n", After: "a\nB\nc\n", ForceDiffer: "summary"})
	if ad.Variants[0].Level != model.LevelSummary {
		t.Fatalf("forced summary should start at Summary, got %v", levels(ad))
	}
}
