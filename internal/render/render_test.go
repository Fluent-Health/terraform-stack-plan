package render

import (
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

func sampleReport() model.Report {
	return model.Report{
		Title:      "Terraform plan — nonprod",
		Marker:     "tfstackplan:nonprod",
		Classified: true,
		Stacks: []model.Stack{
			{
				Name:   "platform/nonprod",
				Counts: model.Counts{Change: 1},
				Class:  &model.Class{Name: "iam", Icon: "🔐"},
				Changes: []model.Change{{
					Address: "google_project_iam_member.editor",
					Type:    "google_project_iam_member",
					Action:  model.ActionChange,
					Attrs: []model.AttrDiff{{
						Name:     "role",
						Variants: []model.Variant{{Level: model.LevelInline, Content: `~ role: "roles/viewer" -> "roles/editor"`}},
					}},
				}},
			},
			{Name: "svc/dev", Counts: model.Counts{Add: 2}, Class: &model.Class{Name: "safe", Icon: "✅"}},
		},
	}
}

func TestRenderMarkerFirst(t *testing.T) {
	out := Render(sampleReport())
	if !strings.HasPrefix(out, "<!-- tfstackplan:nonprod -->\n") {
		t.Fatalf("marker must be line 1, got:\n%s", out[:60])
	}
}

func TestRenderZeroColumnOmitted(t *testing.T) {
	out := Render(sampleReport())
	if strings.Contains(out, "Destroy") || strings.Contains(out, "Replace") {
		t.Fatalf("all-zero columns should be omitted:\n%s", out)
	}
	if !strings.Contains(out, "Add") || !strings.Contains(out, "Change") {
		t.Fatalf("nonzero columns should be present:\n%s", out)
	}
}

func TestRenderClassColumnAndDetails(t *testing.T) {
	out := Render(sampleReport())
	if !strings.Contains(out, "🔐 iam") {
		t.Fatalf("expected class label in table:\n%s", out)
	}
	if !strings.Contains(out, "<details><summary>platform/nonprod") {
		t.Fatalf("expected details for changed stack:\n%s", out)
	}
	if !strings.Contains(out, "```diff") {
		t.Fatalf("expected diff fence:\n%s", out)
	}
}

func TestRenderNoClassColumnWhenUnclassified(t *testing.T) {
	r := sampleReport()
	r.Classified = false
	for i := range r.Stacks {
		r.Stacks[i].Class = nil
	}
	out := Render(r)
	if strings.Contains(out, "Class") {
		t.Fatalf("Class column must be absent when unclassified:\n%s", out)
	}
}

func TestRenderDetailsOpen(t *testing.T) {
	r := sampleReport()
	r.DetailsOpen = true
	out := Render(r)
	if !strings.Contains(out, "<details open><summary>") {
		t.Fatalf("expected open details, got:\n%s", out)
	}
}

func TestRenderSummaryOnlyMode(t *testing.T) {
	r := sampleReport()
	r.Mode = model.ModeSummaryOnly
	r.Notice = "⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact)."
	out := Render(r)
	if strings.Contains(out, "<details>") {
		t.Fatalf("summary-only mode must drop details:\n%s", out)
	}
	if !strings.Contains(out, r.Notice) {
		t.Fatalf("summary-only must include notice")
	}
}

func TestRenderMinimalMode(t *testing.T) {
	r := sampleReport()
	r.Mode = model.ModeMinimal
	r.Notice = "⚠️ Per-stack table omitted: report needs ~120 KB, budget 60 KB."
	out := Render(r)
	if strings.Contains(out, "| Stack") {
		t.Fatalf("minimal mode must drop the table:\n%s", out)
	}
	if !strings.Contains(out, "2 stacks") {
		t.Fatalf("minimal mode should show aggregate count:\n%s", out)
	}
}
