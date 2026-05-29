package fit

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/render"
)

// field builds a block Field whose preferred variant is `big` bytes and which
// can degrade to a tiny summary then hidden.
func field(name string, big int) model.Field {
	return model.Field{
		Name: name,
		Variants: []model.Variant{
			{Level: model.LevelStructural, Content: strings.Repeat("x", big), Bytes: big},
			{Level: model.LevelSummary, Content: "~ " + name + " summary", Bytes: len("~ " + name + " summary")},
			{Level: model.LevelHidden, Content: "", Bytes: 0},
		},
	}
}

func reportWith(fields ...model.Field) *model.Report {
	return &model.Report{
		Title:  "t",
		Marker: "m",
		Stacks: []model.Stack{{
			Name:   "s",
			Counts: model.Counts{Change: 1},
			Changes: []model.Change{{
				Address: "res.a", Type: "t", Action: model.ActionChange, Fields: fields,
			}},
		}},
	}
}

func TestNoChangeWhenUnderBudget(t *testing.T) {
	r := reportWith(field("a", 50))
	before := render.Render(*r)
	Fit(r, 100000)
	if render.Render(*r) != before {
		t.Fatal("under budget: nothing should change")
	}
	if r.Stacks[0].Changes[0].Fields[0].Selected != 0 {
		t.Fatal("under budget: should keep preferred variant")
	}
}

func TestDegradesLargestFirst(t *testing.T) {
	r := reportWith(field("small", 100), field("huge", 5000))
	Fit(r, 800)
	fields := r.Stacks[0].Changes[0].Fields
	if fields[1].Selected < fields[0].Selected {
		t.Fatalf("largest field should degrade first: small=%d huge=%d",
			fields[0].Selected, fields[1].Selected)
	}
	if len(render.Render(*r)) > 800 {
		t.Fatalf("result %d bytes exceeds budget 800", len(render.Render(*r)))
	}
}

func TestDeterministic(t *testing.T) {
	r1 := reportWith(field("a", 4000), field("b", 4000))
	r2 := reportWith(field("a", 4000), field("b", 4000))
	Fit(r1, 500)
	Fit(r2, 500)
	if render.Render(*r1) != render.Render(*r2) {
		t.Fatal("fit must be deterministic for identical input")
	}
}

func TestTerminalSummaryOnly(t *testing.T) {
	r := reportWith(field("a", 50))
	for i := 0; i < 50; i++ {
		r.Stacks = append(r.Stacks, model.Stack{
			Name: "stack-" + itoa(i), Counts: model.Counts{Change: 1},
			Changes: []model.Change{{Address: "r", Type: "t", Action: model.ActionChange, Fields: []model.Field{field("x", 2000)}}},
		})
	}
	Fit(r, 300)
	if r.Mode == model.ModeFull {
		t.Fatalf("expected cascade beyond full mode, got full; size=%d", len(render.Render(*r)))
	}
	if r.Notice == "" {
		t.Fatal("cascade must set a notice")
	}
}

func TestBestEffortFloorReportsOverflow(t *testing.T) {
	r := reportWith(field("a", 5000))
	fits := Fit(r, 10) // absurdly tiny; even minimal line won't fit
	if fits {
		t.Fatal("expected fits=false for a budget too small for even the minimal summary")
	}
	if r.Mode != model.ModeMinimal {
		t.Fatalf("expected ModeMinimal, got %v", r.Mode)
	}
}

func TestFitSkipsLeafFields(t *testing.T) {
	r := &model.Report{Stacks: []model.Stack{{
		Name: "s", Counts: model.Counts{Change: 1},
		Changes: []model.Change{{Address: "a", Action: model.ActionChange,
			Fields: []model.Field{{Name: "x", Leaves: []model.Leaf{{Op: model.OpChange, Path: "x", Old: "1", New: "2"}}}}}},
	}}}
	// A leaf-only report must never panic and always "fits" once small enough.
	_ = Fit(r, 100000)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
