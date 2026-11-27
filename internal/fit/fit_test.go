package fit

import (
	"fmt"
	"math/rand"
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

// refFit is Fit as it was before the size was tracked incrementally: it
// renders the whole report after every step. TestMatchesFullRender checks the
// two make the same choices.
func refFit(r *model.Report, budget int) bool {
	if budget <= 0 {
		return true
	}
	for size(r) > budget {
		_, _, f := largestDegradable(r)
		if f == nil {
			break
		}
		f.Selected++
	}
	if size(r) <= budget {
		return true
	}
	need := size(r)
	r.Mode = model.ModeSummaryOnly
	r.Notice = "⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact)."
	if size(r) <= budget {
		return true
	}
	r.Mode = model.ModeMinimal
	r.Notice = fmt.Sprintf("⚠️ Per-stack table omitted: report needs ~%s, budget %s.", humanBytes(need), humanBytes(budget))
	return size(r) <= budget
}

// randomReport builds a report with several stacks (some with no changes),
// leaf and block fields, multi-line and blank-line variant content, and ties
// in variant sizes.
func randomReport(rng *rand.Rand) *model.Report {
	lines := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			if rng.Intn(5) == 0 {
				b.WriteString("\n") // blank line inside the diff
				continue
			}
			b.WriteString(strings.Repeat("y", 1+rng.Intn(40)) + "\n")
		}
		return b.String()
	}
	r := &model.Report{Title: "t", Marker: "m", DetailsOpen: rng.Intn(2) == 0}
	for si := 0; si < 1+rng.Intn(5); si++ {
		s := model.Stack{Name: "stack-" + itoa(si)}
		if rng.Intn(5) != 0 {
			s.Counts = model.Counts{Change: 1}
		}
		for ci := 0; ci < rng.Intn(6); ci++ {
			c := model.Change{Address: "res." + itoa(ci), Type: "t", Action: model.ActionChange}
			if rng.Intn(3) == 0 {
				c.Fields = append(c.Fields, model.Field{Name: "leaf", Leaves: []model.Leaf{{Op: model.OpChange, Path: "x", Old: "1", New: "2"}}})
			}
			for fi := 0; fi < rng.Intn(4); fi++ {
				var vs []model.Variant
				for vi := 0; vi < 1+rng.Intn(4); vi++ {
					content := lines(rng.Intn(15))
					vs = append(vs, model.Variant{Level: model.LevelStructural, Content: content, Bytes: len(content) / (1 + rng.Intn(2))})
				}
				vs = append(vs, model.Variant{Level: model.LevelHidden})
				c.Fields = append(c.Fields, model.Field{Name: "f" + itoa(fi), Variants: vs})
			}
			s.Changes = append(s.Changes, c)
		}
		r.Stacks = append(r.Stacks, s)
	}
	return r
}

func TestMatchesFullRender(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		a := randomReport(rng)
		full := len(render.Render(*a))
		budget := rng.Intn(full + 200)
		want := deepCopy(a)
		wantFits := refFit(want, budget)
		gotFits := Fit(a, budget)
		if gotFits != wantFits || render.Render(*a) != render.Render(*want) || !sameSelection(a, want) {
			t.Fatalf("case %d (budget %d): incremental fit differs from full-render fit", i, budget)
		}
	}
}

func deepCopy(r *model.Report) *model.Report {
	c := *r
	c.Stacks = make([]model.Stack, len(r.Stacks))
	for si, s := range r.Stacks {
		c.Stacks[si] = s
		c.Stacks[si].Changes = make([]model.Change, len(s.Changes))
		for ci, ch := range s.Changes {
			c.Stacks[si].Changes[ci] = ch
			c.Stacks[si].Changes[ci].Fields = append([]model.Field(nil), ch.Fields...)
		}
	}
	return &c
}

func sameSelection(a, b *model.Report) bool {
	for si := range a.Stacks {
		for ci := range a.Stacks[si].Changes {
			for fi := range a.Stacks[si].Changes[ci].Fields {
				if a.Stacks[si].Changes[ci].Fields[fi].Selected != b.Stacks[si].Changes[ci].Fields[fi].Selected {
					return false
				}
			}
		}
	}
	return true
}
