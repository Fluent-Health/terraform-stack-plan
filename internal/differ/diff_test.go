package differ

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

func levels(f model.Field) []model.Level {
	var out []model.Level
	for _, v := range f.Variants {
		out = append(out, v.Level)
	}
	return out
}

func TestScalarLeaf(t *testing.T) {
	f := Diff(Input{Attr: "role", Before: "roles/viewer", After: "roles/editor"})
	if f.IsBlock() || len(f.Leaves) != 1 {
		t.Fatalf("scalar should be one leaf, got block=%v leaves=%d", f.IsBlock(), len(f.Leaves))
	}
	l := f.Leaves[0]
	if l.Op != model.OpChange || l.Path != "role" || l.Value() != `"roles/viewer" → "roles/editor"` {
		t.Fatalf("unexpected leaf: %+v value=%q", l, l.Value())
	}
}

func TestSensitiveLeaf(t *testing.T) {
	f := Diff(Input{Attr: "password", Before: "a", After: "b", Sensitive: true})
	if len(f.Leaves) != 1 || f.Leaves[0].Inline != "(sensitive value)" {
		t.Fatalf("sensitive should be one leaf with inline marker, got %+v", f.Leaves)
	}
}

func TestUnknownLeaf(t *testing.T) {
	f := Diff(Input{Attr: "id", Unknown: true})
	if len(f.Leaves) != 1 || f.Leaves[0].Inline != "(known after apply)" {
		t.Fatalf("unknown should be one leaf with inline marker, got %+v", f.Leaves)
	}
}

func TestCreateLeaf(t *testing.T) {
	f := Diff(Input{Attr: "account_id", After: "app-api"})
	if len(f.Leaves) != 1 || f.Leaves[0].Op != model.OpAdd || f.Leaves[0].Value() != `"app-api"` {
		t.Fatalf("create-only value should be an add leaf, got %+v", f.Leaves)
	}
}

func TestDeleteLeaf(t *testing.T) {
	f := Diff(Input{Attr: "name", Before: "legacy"})
	if len(f.Leaves) != 1 || f.Leaves[0].Op != model.OpRemove || f.Leaves[0].Value() != `"legacy"` {
		t.Fatalf("delete-only value should be a remove leaf, got %+v", f.Leaves)
	}
}

func TestStructuredYAMLLadder(t *testing.T) {
	// Use >= foldThreshold changed keys so the diff stays a block ladder.
	var bb, ab strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&bb, "  k%02d: old\n", i)
		fmt.Fprintf(&ab, "  k%02d: new\n", i)
	}
	before := "spec:\n" + bb.String()
	after := "spec:\n" + ab.String()
	f := Diff(Input{Attr: "manifest", Before: before, After: after})
	want := []model.Level{model.LevelStructural, model.LevelSummary, model.LevelHidden}
	if got := levels(f); !equalLevels(got, want) {
		t.Fatalf("yaml ladder = %v, want %v", got, want)
	}
}

func TestForcedDifferLine(t *testing.T) {
	before := "a: 1\n"
	after := "a: 2\n"
	f := Diff(Input{Attr: "manifest", Before: before, After: after, ForceDiffer: "line"})
	if levels(f)[0] != model.LevelLineDiff {
		t.Fatalf("forced line differ should start with LineDiff, got %v", levels(f))
	}
}

func TestMaxAttributeLinesCeiling(t *testing.T) {
	// Use >= foldThreshold changed keys so the diff is a block (not leaves),
	// then MaxLines forces the block ladder to start at Summary.
	var bb, ab strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&bb, "  k%02d: old\n", i)
		fmt.Fprintf(&ab, "  k%02d: new\n", i)
	}
	before := "spec:\n" + bb.String()
	after := "spec:\n" + ab.String()
	f := Diff(Input{Attr: "manifest", Before: before, After: after, MaxLines: 1})
	if levels(f)[0] != model.LevelSummary {
		t.Fatalf("tiny cap should start at Summary, got %v", levels(f))
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
	// Use >= foldThreshold changed keys so the diff stays a block ladder.
	before := map[string]any{}
	after := map[string]any{}
	for i := 0; i < 12; i++ {
		before[fmt.Sprintf("k%02d", i)] = fmt.Sprintf("old%d", i)
		after[fmt.Sprintf("k%02d", i)] = fmt.Sprintf("new%d", i)
	}
	f := Diff(Input{Attr: "settings", Before: before, After: after})
	// ladder should be [Structural, Summary, Hidden]
	if len(f.Variants) != 3 {
		t.Fatalf("want 3 variants, got %d (%v)", len(f.Variants), levels(f))
	}
	sum := f.Variants[1]
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
	f := Diff(Input{Attr: "data", Before: before, After: after})
	want := []model.Level{model.LevelSummary, model.LevelHidden}
	if got := levels(f); !equalLevels(got, want) {
		t.Fatalf("base64 ladder = %v, want %v", got, want)
	}
	if !strings.Contains(f.Variants[0].Content, "base64") {
		t.Fatalf("base64 summary should mention base64: %q", f.Variants[0].Content)
	}
}

func TestNoDetectForcesLine(t *testing.T) {
	before := "spec:\n  replicas: 3\n  image: v1\n"
	after := "spec:\n  replicas: 5\n  image: v1\n"
	// Without NoDetect this YAML string would start Structural; with NoDetect it must start LineDiff.
	f := Diff(Input{Attr: "manifest", Before: before, After: after, NoDetect: true})
	if levels(f)[0] != model.LevelLineDiff {
		t.Fatalf("NoDetect should force LineDiff, got %v", levels(f))
	}
}

func TestForcedSummary(t *testing.T) {
	f := Diff(Input{Attr: "x", Before: "a\nb\nc\n", After: "a\nB\nc\n", ForceDiffer: "summary"})
	if f.Variants[0].Level != model.LevelSummary {
		t.Fatalf("forced summary should start at Summary, got %v", levels(f))
	}
}

func TestStructuralSmallBecomesLeaves(t *testing.T) {
	before := map[string]any{"env": "nonprod"}
	after := map[string]any{"env": "nonprod", "team": "platform"}
	f := Diff(Input{Attr: "labels", Before: before, After: after})
	if f.IsBlock() {
		t.Fatalf("small structural diff should be leaves, not a block")
	}
	if len(f.Leaves) != 1 {
		t.Fatalf("want 1 changed leaf, got %d (%+v)", len(f.Leaves), f.Leaves)
	}
	l := f.Leaves[0]
	if l.Op != model.OpAdd || l.Path != "labels.team" || l.Value() != `"platform"` {
		t.Fatalf("leaf should be `+ labels.team = \"platform\"`, got %+v value=%q", l, l.Value())
	}
}

func TestStructuralLargeStaysBlock(t *testing.T) {
	before := map[string]any{}
	after := map[string]any{}
	for i := 0; i < 40; i++ {
		before[fmt.Sprintf("k%02d", i)] = "old"
		after[fmt.Sprintf("k%02d", i)] = "new"
	}
	f := Diff(Input{Attr: "settings", Before: before, After: after})
	if !f.IsBlock() {
		t.Fatalf("40-leaf structural diff should stay a foldable block")
	}
}

func TestStructuralFoldBoundary(t *testing.T) {
	mk := func(n int) (map[string]any, map[string]any) {
		b, a := map[string]any{}, map[string]any{}
		for i := 0; i < n; i++ {
			b[fmt.Sprintf("k%02d", i)] = "old"
			a[fmt.Sprintf("k%02d", i)] = "new"
		}
		return b, a
	}
	b9, a9 := mk(9)
	if f := Diff(Input{Attr: "s", Before: b9, After: a9}); f.IsBlock() {
		t.Errorf("9 changed leaves should stay inline leaves, not a block")
	}
	b10, a10 := mk(10)
	if f := Diff(Input{Attr: "s", Before: b10, After: a10}); !f.IsBlock() {
		t.Errorf("10 changed leaves should fold into a block")
	}
}

func TestStructuralLeafOps(t *testing.T) {
	before := map[string]any{"keep": "x", "gone": "y", "num": 7.0}
	after := map[string]any{"keep": "x", "num": 9.0, "added": "z"}
	f := Diff(Input{Attr: "m", Before: before, After: after})
	if f.IsBlock() {
		t.Fatalf("small diff should be leaves")
	}
	got := map[string]model.Leaf{}
	for _, l := range f.Leaves {
		got[l.Path] = l
	}
	if l, ok := got["m.gone"]; !ok || l.Op != model.OpRemove || l.Value() != `"y"` {
		t.Errorf("m.gone should be removed leaf, got %+v", l)
	}
	if l, ok := got["m.num"]; !ok || l.Op != model.OpChange || l.Value() != "7 → 9" {
		t.Errorf("m.num should be change leaf 7 → 9, got %+v value=%q", l, l.Value())
	}
	if l, ok := got["m.added"]; !ok || l.Op != model.OpAdd || l.Value() != `"z"` {
		t.Errorf("m.added should be add leaf, got %+v", l)
	}
	if _, ok := got["m.keep"]; ok {
		t.Errorf("unchanged key should not appear")
	}
}
