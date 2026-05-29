package fit

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/render"
)

// attr builds an AttrDiff whose preferred variant is `big` bytes and which can
// degrade to a tiny summary then hidden.
func attr(name string, big int) model.AttrDiff {
	return model.AttrDiff{
		Name: name,
		Variants: []model.Variant{
			{Level: model.LevelStructural, Content: strings.Repeat("x", big), Bytes: big},
			{Level: model.LevelSummary, Content: "~ " + name + " summary", Bytes: len("~ " + name + " summary")},
			{Level: model.LevelHidden, Content: "", Bytes: 0},
		},
	}
}

func reportWith(attrs ...model.AttrDiff) *model.Report {
	return &model.Report{
		Title:  "t",
		Marker: "m",
		Stacks: []model.Stack{{
			Name:   "s",
			Counts: model.Counts{Change: 1},
			Changes: []model.Change{{
				Address: "res.a", Type: "t", Action: model.ActionChange, Attrs: attrs,
			}},
		}},
	}
}

func TestNoChangeWhenUnderBudget(t *testing.T) {
	r := reportWith(attr("a", 50))
	before := render.Render(*r)
	Fit(r, 100000)
	if render.Render(*r) != before {
		t.Fatal("under budget: nothing should change")
	}
	if r.Stacks[0].Changes[0].Attrs[0].Selected != 0 {
		t.Fatal("under budget: should keep preferred variant")
	}
}

func TestDegradesLargestFirst(t *testing.T) {
	r := reportWith(attr("small", 100), attr("huge", 5000))
	Fit(r, 800)
	attrs := r.Stacks[0].Changes[0].Attrs
	if attrs[1].Selected < attrs[0].Selected {
		t.Fatalf("largest attr should degrade first: small=%d huge=%d",
			attrs[0].Selected, attrs[1].Selected)
	}
	if len(render.Render(*r)) > 800 {
		t.Fatalf("result %d bytes exceeds budget 800", len(render.Render(*r)))
	}
}

func TestDeterministic(t *testing.T) {
	r1 := reportWith(attr("a", 4000), attr("b", 4000))
	r2 := reportWith(attr("a", 4000), attr("b", 4000))
	Fit(r1, 500)
	Fit(r2, 500)
	if render.Render(*r1) != render.Render(*r2) {
		t.Fatal("fit must be deterministic for identical input")
	}
}

func TestTerminalSummaryOnly(t *testing.T) {
	r := reportWith(attr("a", 50))
	for i := 0; i < 50; i++ {
		r.Stacks = append(r.Stacks, model.Stack{
			Name: "stack-" + itoa(i), Counts: model.Counts{Change: 1},
			Changes: []model.Change{{Address: "r", Type: "t", Action: model.ActionChange, Attrs: []model.AttrDiff{attr("x", 2000)}}},
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
	r := reportWith(attr("a", 5000))
	fits := Fit(r, 10) // absurdly tiny; even minimal line won't fit
	if fits {
		t.Fatal("expected fits=false for a budget too small for even the minimal summary")
	}
	if r.Mode != model.ModeMinimal {
		t.Fatalf("expected ModeMinimal, got %v", r.Mode)
	}
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
