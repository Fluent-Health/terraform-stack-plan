package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

func sampleReport() model.Report {
	return model.Report{
		Title:      "Terraform plan — nonprod",
		Marker:     "tfstackplan:nonprod",
		Classified: true,
		Default:    model.Class{Name: "safe", Icon: "✅"},
		Stacks: []model.Stack{
			{
				Name:       "platform/nonprod",
				Counts:     model.Counts{Change: 1},
				Categories: []model.Class{{Name: "iam", Icon: "🔐"}, {Name: "destructive", Icon: "💣"}},
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
			{Name: "svc/dev", Counts: model.Counts{Add: 2}}, // no Categories → renders the default
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

func TestRenderCategoriesColumnAndDetails(t *testing.T) {
	out := Render(sampleReport())
	if !strings.Contains(out, "Categories") {
		t.Fatalf("expected Categories column header:\n%s", out)
	}
	if !strings.Contains(out, "🔐 iam") || !strings.Contains(out, "💣 destructive") {
		t.Fatalf("expected both category badges in the multi-category row:\n%s", out)
	}
	if !strings.Contains(out, "🔐 iam  💣 destructive") {
		t.Fatalf("expected both badges joined by two spaces in one cell:\n%s", out)
	}
	if !strings.Contains(out, "<b>svc/dev</b> · ✅ safe") {
		t.Fatalf("expected the default badge in the details summary of a no-category stack:\n%s", out)
	}
	if !strings.Contains(out, "✅ safe") {
		t.Fatalf("expected the default badge for the no-category stack:\n%s", out)
	}
	if !strings.Contains(out, "<details><summary>📁&nbsp;<b>platform/nonprod</b>") {
		t.Fatalf("expected details for changed stack:\n%s", out)
	}
	if !strings.Contains(out, "```diff") {
		t.Fatalf("expected diff fence:\n%s", out)
	}
}

func TestRenderNoCategoriesColumnWhenUnclassified(t *testing.T) {
	r := sampleReport()
	r.Classified = false
	for i := range r.Stacks {
		r.Stacks[i].Categories = nil
	}
	out := Render(r)
	if strings.Contains(out, "Categories") {
		t.Fatalf("Categories column must be absent when unclassified:\n%s", out)
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

func TestRenderCreateSmallIsOpenRow(t *testing.T) {
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
	// Small create (2 attrs) is an open row with its attributes shown.
	if !strings.Contains(out, "<details open><summary>"+glyphAdd+"&nbsp;google_service_account.api<br>"+metaIndent+"2 attrs</summary>") {
		t.Fatalf("small create should be an open row:\n%s", out)
	}
	if !strings.Contains(out, "+ account_id = \"app-api\"") {
		t.Fatalf("create body should list attributes:\n%s", out)
	}
}

func TestRenderResourceClosedWhenBig(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&big, "- line %d\n+ line %d\n", i, i)
	}
	r := sampleReport()
	r.Stacks[0].Changes = []model.Change{{
		Address: "kubernetes_config_map.app",
		Type:    "kubernetes_config_map",
		Action:  model.ActionChange,
		Fields: []model.Field{{
			Name:     "data",
			Variants: []model.Variant{{Level: model.LevelLineDiff, Content: big.String(), Bytes: big.Len()}},
		}},
	}}
	r.Stacks[0].Counts = model.Counts{Change: 1}
	out := Render(r)
	if !strings.Contains(out, "<details><summary>"+glyphChange+"&nbsp;kubernetes_config_map.app<br>"+metaIndent+"1 changed</summary>") {
		t.Fatalf("big resource should be a closed row:\n%s", out)
	}
	if strings.Contains(out, "<details open><summary>"+glyphChange+"&nbsp;kubernetes_config_map.app") {
		t.Fatalf("big resource must not be open:\n%s", out)
	}
	if !strings.Contains(out, "+ line 0") {
		t.Fatalf("closed row should still contain the diff body:\n%s", out)
	}
}

func TestRenderBlockquoteBar(t *testing.T) {
	out := Render(sampleReport())
	if !strings.Contains(out, "> <details") {
		t.Fatalf("resource rows should be wrapped in a stack blockquote:\n%s", out)
	}
}

func TestRenderStackHeaderAndSpacing(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Counts = model.Counts{Add: 2}
	r.Stacks[0].Changes = []model.Change{
		{Address: "x.a", Action: model.ActionAdd, Fields: []model.Field{{Name: "n", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "n", New: `"1"`}}}}},
		{Address: "x.b", Action: model.ActionAdd, Fields: []model.Field{{Name: "n", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "n", New: `"2"`}}}}},
	}
	out := Render(r)
	// Stack header is a folder icon + bold name, distinct from the resource rows.
	if !strings.Contains(out, "<summary>📁&nbsp;<b>platform/nonprod</b>") {
		t.Fatalf("stack summary should be folder icon + bold name:\n%s", out)
	}
	// A blank quoted line gives the stack title room above the first row.
	if !strings.Contains(out, "</summary>\n\n>\n> <details") {
		t.Fatalf("expected a blank quoted line between the stack title and first row:\n%s", out)
	}
	// A blank quoted line separates consecutive resource rows.
	if !strings.Contains(out, "</details>\n>\n> <details") {
		t.Fatalf("expected a blank quoted line between resource rows:\n%s", out)
	}
}

func TestRenderForgetRow(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Counts = model.Counts{Forget: 1}
	r.Stacks[0].Changes = []model.Change{{
		Address: "aws_s3_bucket.legacy", Action: model.ActionForget,
		Fields: []model.Field{
			{Name: "bucket", Leaves: []model.Leaf{{Op: model.OpRemove, Path: "bucket", Old: `"legacy"`}}},
			{Name: "region", Leaves: []model.Leaf{{Op: model.OpRemove, Path: "region", Old: `"us-east-1"`}}},
		},
	}}
	out := Render(r)
	if !strings.Contains(out, glyphForget+"&nbsp;aws_s3_bucket.legacy<br>"+metaIndent+"forgotten · 2 attrs") {
		t.Fatalf("forget row label wrong:\n%s", out)
	}
	if !strings.Contains(out, "⊘ bucket = \"legacy\"") {
		t.Fatalf("forget body should use ⊘ glyph:\n%s", out)
	}
}

func TestRenderMovedRow(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Counts = model.Counts{Move: 1}
	r.Stacks[0].Changes = []model.Change{{
		Address: "google_storage_bucket.assets", Action: model.ActionNoop,
		Moved: true, PreviousAddress: "google_storage_bucket.legacy_assets",
	}}
	out := Render(r)
	if !strings.Contains(out, glyphMoved+"&nbsp;google_storage_bucket.assets<br>"+metaIndent+"moved from google_storage_bucket.legacy_assets") {
		t.Fatalf("moved row label wrong:\n%s", out)
	}
	if !strings.Contains(out, "(address change only)") {
		t.Fatalf("pure move should note address-only:\n%s", out)
	}
}

func TestRenderDirectionalMovedRows(t *testing.T) {
	// Move-out test
	r1 := sampleReport()
	r1.Stacks[0].Counts = model.Counts{Move: 1}
	r1.Stacks[0].Changes = []model.Change{{
		Address: "google_storage_bucket.assets", Action: model.ActionNoop,
		Moved: true, MoveDirection: "out",
	}}
	out1 := Render(r1)
	want1 := glyphMoveOut + "&nbsp;google_storage_bucket.assets<br>" + metaIndent + "moved out (relocating)"
	if !strings.Contains(out1, want1) {
		t.Fatalf("moved out row label wrong:\n%s", out1)
	}

	// Move-in test
	r2 := sampleReport()
	r2.Stacks[0].Counts = model.Counts{Move: 1}
	r2.Stacks[0].Changes = []model.Change{{
		Address: "google_storage_bucket.assets", Action: model.ActionNoop,
		Moved: true, MoveDirection: "in",
	}}
	out2 := Render(r2)
	want2 := glyphMoveIn + "&nbsp;google_storage_bucket.assets<br>" + metaIndent + "moved in (cross-state)"
	if !strings.Contains(out2, want2) {
		t.Fatalf("moved in row label wrong:\n%s", out2)
	}

	// Move + Update test
	r3 := sampleReport()
	r3.Stacks[0].Counts = model.Counts{Move: 1}
	r3.Stacks[0].Changes = []model.Change{{
		Address: "google_storage_bucket.assets", Action: model.ActionNoop,
		Moved: true, MoveDirection: "in-update",
		Fields: []model.Field{
			{Name: "labels", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "labels.team", New: `"platform"`}}},
		},
	}}
	out3 := Render(r3)
	want3 := glyphMoveUpd + "&nbsp;google_storage_bucket.assets<br>" + metaIndent + "moved in and updated"
	if !strings.Contains(out3, want3) {
		t.Fatalf("moved and updated row label wrong:\n%s", out3)
	}
	if !strings.Contains(out3, "+ labels.team = \"platform\"") {
		t.Fatalf("moved and updated diff body wrong:\n%s", out3)
	}
}

func TestRenderImportedRow(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Counts = model.Counts{Import: 1}
	r.Stacks[0].Changes = []model.Change{{
		Address: "google_project.host", Action: model.ActionNoop,
		Imported: true, ImportID: "my-host-project",
	}}
	out := Render(r)
	want := glyphImported + "&nbsp;google_project.host<br>" + metaIndent + "imported · id=<code>my-host-project</code>"
	if !strings.Contains(out, want) {
		t.Fatalf("imported row label wrong:\n%s", out)
	}
}

func TestRenderTableExtrasSuffix(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].Counts = model.Counts{Move: 1, Import: 1, Forget: 1}
	out := Render(r)
	if !strings.Contains(out, "platform/nonprod · 1 import, 1 move, 1 forget |") {
		t.Fatalf("table Stack cell should carry move/import/forget suffix:\n%s", out)
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
	if !strings.Contains(out, metaIndent+"2 attrs") {
		t.Fatalf("attr count should be 2 (fields), got:\n%s", out)
	}
	if !strings.Contains(out, "+ a") || !strings.Contains(out, "+ b") {
		t.Fatalf("block field content must not be dropped:\n%s", out)
	}
}

func TestRenderBlockFieldInResourceBody(t *testing.T) {
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
	// The block field renders inside the resource row's body (labelled "~ data:"),
	// not as a separate sub-details. The resource row itself is the only fold level.
	if !strings.Contains(out, "~ data:") {
		t.Fatalf("block field should be labelled in the resource body:\n%s", out)
	}
	if !strings.Contains(out, "- old") || !strings.Contains(out, "+ new") {
		t.Fatalf("resource body should contain the selected variant's diff:\n%s", out)
	}
	if strings.Contains(out, "<details open><summary>~ data") || strings.Contains(out, "<details><summary>~ data") {
		t.Fatalf("block field must not be a separate sub-details:\n%s", out)
	}
}

func TestRenderHeaderLinks(t *testing.T) {
	r := sampleReport()
	r.HeaderLinks = []model.Link{{Label: "PR #1", URL: "https://x/1"}, {Label: "Build", URL: "https://x/b"}}
	out := Render(r)
	if !strings.Contains(out, "[PR #1](https://x/1) · [Build](https://x/b)") {
		t.Fatalf("header links line missing:\n%s", out)
	}
}

func TestRenderStackAndResourceLinks(t *testing.T) {
	r := sampleReport()
	r.Stacks[0].URL = "https://x/stack"
	r.Stacks[0].Changes[0].URL = "https://x/res"
	out := Render(r)
	if !strings.Contains(out, `<a href="https://x/stack">platform/nonprod</a>`) {
		t.Fatalf("stack link missing:\n%s", out)
	}
	if !strings.Contains(out, `<a href="https://x/res">google_storage_bucket.tfstate</a>`) {
		t.Fatalf("resource link missing:\n%s", out)
	}
}

func TestPerStack(t *testing.T) {
	r := model.Report{
		Title: "T", Marker: "m",
		Stacks: []model.Stack{
			{Name: "stacks/a", Counts: model.Counts{Add: 1}, Changes: []model.Change{
				{Address: "aws_s3_bucket.a", Type: "aws_s3_bucket", Action: model.ActionAdd},
			}},
			{Name: "stacks/b", Counts: model.Counts{Change: 1}, Changes: []model.Change{
				{Address: "aws_iam_role.b", Type: "aws_iam_role", Action: model.ActionChange},
			}},
			{Name: "stacks/c"}, // no change → excluded
		},
	}
	per := PerStack(r)
	if len(per) != 2 {
		t.Fatalf("PerStack = %d entries, want 2 (no-change stack excluded)", len(per))
	}
	if !strings.Contains(per["stacks/a"], "aws_s3_bucket.a") {
		t.Errorf("stacks/a section missing its resource: %q", per["stacks/a"])
	}
	if !strings.Contains(per["stacks/b"], "aws_iam_role.b") {
		t.Errorf("stacks/b section missing its resource: %q", per["stacks/b"])
	}
	if _, ok := per["stacks/c"]; ok {
		t.Error("no-change stack should be excluded")
	}
}

func TestRenderNoTable(t *testing.T) {
	r := sampleReport()
	full := Render(r)
	noTable := RenderNoTable(r)

	// Render must include the table header (Stack column).
	if !strings.Contains(full, "| Stack |") {
		t.Fatalf("Render must include the summary table:\n%s", full)
	}
	// RenderNoTable must NOT include the table.
	if strings.Contains(noTable, "| Stack |") {
		t.Fatalf("RenderNoTable must not include the summary table:\n%s", noTable)
	}
	// RenderNoTable must include the header (### ...).
	if !strings.Contains(noTable, "### ") {
		t.Fatalf("RenderNoTable must include the header (###):\n%s", noTable)
	}
	// RenderNoTable must include the per-stack details section.
	if !strings.Contains(noTable, "<details") {
		t.Fatalf("RenderNoTable must include per-stack change trees:\n%s", noTable)
	}
}

func TestRenderEmptyReport(t *testing.T) {
	r := model.Report{
		Title:       "Terraform plan — nonprod",
		Marker:      "tf-plan:nonprod",
		Classified:  true,
		Stacks:      nil,
		HeaderLinks: []model.Link{{Label: "PR #42", URL: "https://gh/o/r/pull/42"}},
	}
	out := Render(r)
	if !strings.HasPrefix(out, "<!-- tf-plan:nonprod -->\n") {
		t.Fatalf("missing marker line:\n%s", out)
	}
	if !strings.Contains(out, "### Terraform plan — nonprod  (0 stacks changed)") {
		t.Fatalf("missing 0-stacks heading:\n%s", out)
	}
	if strings.Contains(out, "| Stack |") {
		t.Fatalf("empty report must not render a table:\n%s", out)
	}
	if !strings.Contains(out, "[PR #42](https://gh/o/r/pull/42)") {
		t.Fatalf("header links should still render:\n%s", out)
	}
}

func TestRenderSensitivityOnly(t *testing.T) {
	// 1. Verify renderMinimal formatting containing "7 changes (7 sensitivity-only)"
	rMinimal := model.Report{
		Title:  "Terraform plan — nonprod",
		Marker: "tfstackplan:nonprod",
		Mode:   model.ModeMinimal,
		Stacks: []model.Stack{
			{
				Name:   "platform/nonprod",
				Counts: model.Counts{Change: 7, SensitivityOnly: 7},
			},
		},
	}
	outMinimal := Render(rMinimal)
	wantMinimal := "7 changes (7 sensitivity-only)"
	if !strings.Contains(outMinimal, wantMinimal) {
		t.Errorf("expected minimal output to contain %q, got:\n%s", wantMinimal, outMinimal)
	}

	// 2. Verify changeWord formatting containing "7 change (7 sensitivity-only)"
	rDetails := model.Report{
		Title:  "Terraform plan — nonprod",
		Marker: "tfstackplan:nonprod",
		Stacks: []model.Stack{
			{
				Name:   "platform/nonprod",
				Counts: model.Counts{Change: 7, SensitivityOnly: 7},
				Changes: []model.Change{
					{
						Address:         "google_storage_bucket.tfstate",
						Type:            "google_storage_bucket",
						Action:          model.ActionChange,
						SensitivityOnly: true,
						Fields: []model.Field{
							{Name: "labels", Leaves: []model.Leaf{{Op: model.OpAdd, Path: "labels.team", New: `"platform"`}}},
						},
					},
				},
			},
		},
	}
	outDetails := Render(rDetails)
	wantChangeWord := "7 change (7 sensitivity-only)"
	if !strings.Contains(outDetails, wantChangeWord) {
		t.Errorf("expected changeWord output to contain %q, got:\n%s", wantChangeWord, outDetails)
	}

	// 3. Verify resourceSummary rendering "sensitivity change · 1 attrs"
	wantResourceSummary := "sensitivity change · 1 attrs"
	if !strings.Contains(outDetails, wantResourceSummary) {
		t.Errorf("expected resourceSummary output to contain %q, got:\n%s", wantResourceSummary, outDetails)
	}
}
