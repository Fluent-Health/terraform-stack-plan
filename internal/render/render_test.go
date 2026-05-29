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
					Address: "google_storage_bucket.tfstate",
					Type:    "google_storage_bucket",
					Action:  model.ActionChange,
					Fields: []model.Field{
						{Name: "labels", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "labels.team", New: `"platform"`}}},
						{Name: "retention_days", Leaves: []model.Leaf{{Op: model.OpChange, Path: "retention_days", Old: "7", New: "30"}}},
					},
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

func TestRenderUpdateLeavesAligned(t *testing.T) {
	out := Render(sampleReport())
	if !strings.Contains(out, "+ labels.team    = \"platform\"") {
		t.Fatalf("labels.team not aligned:\n%s", out)
	}
	if !strings.Contains(out, "~ retention_days = 7 → 30") {
		t.Fatalf("retention_days line wrong:\n%s", out)
	}
}

func TestRenderCreateFolds(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Changes = []model.Change{{
		Address: "google_service_account.api",
		Type:    "google_service_account",
		Action:  model.ActionAdd,
		Fields: []model.Field{
			{Name: "account_id", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "account_id", New: `"app-api"`}}},
			{Name: "disabled", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "disabled", New: "false"}}},
		},
	}}
	r.Stacks[0].Counts = model.Counts{Add: 1}
	out := Render(r)
	if !strings.Contains(out, "<details><summary>+ google_service_account.api · 2 attrs</summary>") {
		t.Fatalf("create should fold into a nested details:\n%s", out)
	}
	if !strings.Contains(out, "+ account_id = \"app-api\"") {
		t.Fatalf("create body should list attributes:\n%s", out)
	}
}

func TestRenderCreateBlockFieldRendered(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Changes = []model.Change{{
		Address: "x.y", Action: model.ActionAdd,
		Fields: []model.Field{
			{Name: "n", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "n", New: `"1"`}}},
			{Name: "data", Variants: []model.Variant{{Level: model.LevelLineDiff, Content: "+ a\n+ b", Bytes: 4}}},
		},
	}}
	r.Stacks[0].Counts = model.Counts{Add: 1}
	out := Render(r)
	if !strings.Contains(out, "· 2 attrs") {
		t.Fatalf("attr count should be 2 (fields), got:\n%s", out)
	}
	if !strings.Contains(out, "+ a") || !strings.Contains(out, "+ b") {
		t.Fatalf("block field content must not be dropped:\n%s", out)
	}
}

func TestRenderLargeAttrFolds(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Changes = []model.Change{{
		Address: "kubernetes_config_map.app",
		Type:    "kubernetes_config_map",
		Action:  model.ActionChange,
		Fields: []model.Field{{
			Name: "data",
			Variants: []model.Variant{
				{Level: model.LevelLineDiff, Content: "- old\n+ new", Bytes: 9},
				{Level: model.LevelSummary, Content: "  ~ data · text · 2 lines · 2 changed (hidden to fit size limit)"},
				{Level: model.LevelHidden, Content: ""},
			},
		}},
	}}
	r.Stacks[0].Counts = model.Counts{Change: 1}
	out := Render(r)
	if !strings.Contains(out, "<details><summary>~ data") {
		t.Fatalf("large attr should fold into a nested details:\n%s", out)
	}
	if !strings.Contains(out, "- old") || !strings.Contains(out, "+ new") {
		t.Fatalf("folded block should contain the selected variant's diff:\n%s", out)
	}
}
