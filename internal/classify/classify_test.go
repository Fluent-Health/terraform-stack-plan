package classify

import (
	"regexp"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
)

func stack(changes ...plan.RawChange) plan.RawStack {
	return plan.RawStack{Name: "s", Changes: changes}
}

func TestFirstHitWins(t *testing.T) {
	rules := []Rule{
		{Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1},
		{Name: "destructive", Icon: "💣", Actions: []string{"delete"}, MinCount: 1},
	}
	def := model.Class{Name: "safe"}

	iamChange := plan.RawChange{Type: "google_project_iam_member", Actions: []string{"update"}}
	got := Classify(stack(iamChange), rules, def)
	if got.Class.Name != "iam" {
		t.Fatalf("update to iam resource should classify iam, got %q", got.Class.Name)
	}
}

func TestActionsAndMinCount(t *testing.T) {
	rules := []Rule{{Name: "destructive", Actions: []string{"delete"}, MinCount: 2}}
	def := model.Class{Name: "safe"}

	oneDelete := stack(plan.RawChange{Type: "x", Actions: []string{"delete"}})
	if Classify(oneDelete, rules, def).Class.Name != "safe" {
		t.Fatal("one delete should not meet min_count 2")
	}
	twoDeletes := stack(
		plan.RawChange{Type: "x", Actions: []string{"delete"}},
		plan.RawChange{Type: "y", Actions: []string{"delete"}},
	)
	if Classify(twoDeletes, rules, def).Class.Name != "destructive" {
		t.Fatal("two deletes should meet min_count 2")
	}
}

func TestDefaultWhenNoMatch(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	def := model.Class{Name: "safe", Icon: "✅"}
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}}), rules, def)
	if got.Class != def {
		t.Fatalf("no match should yield default %+v, got %+v", def, got.Class)
	}
}

func TestEmitAttributesFromMatchedChangesOnly(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	def := model.Class{Name: "safe"}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"update"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	got := Classify(s, rules, def)
	if got.Class.Name != "iam" {
		t.Fatalf("class = %q, want iam", got.Class.Name)
	}
	if len(got.Attributes["project"]) != 1 || got.Attributes["project"][0] != "p1" {
		t.Fatalf("project = %v, want [p1] (bucket's p2 must NOT appear)", got.Attributes["project"])
	}
}

func TestEmitAttributesDedupeAndSort(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	got := Classify(s, rules, model.Class{Name: "safe"})
	want := []string{"p1", "p2"}
	if len(got.Attributes["project"]) != 2 || got.Attributes["project"][0] != want[0] || got.Attributes["project"][1] != want[1] {
		t.Fatalf("project = %v, want %v", got.Attributes["project"], want)
	}
}

func TestEmitAttributesNilWhenNoneConfigured(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	got := Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules, model.Class{Name: "safe"})
	if got.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no emit_attributes", got.Attributes)
	}
}

func TestEmitAttributesNilWhenNoValuesFound(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	// org-level binding: matches iam, but has no "project" attribute.
	got := Classify(
		stack(plan.RawChange{Type: "google_organization_iam_binding", Actions: []string{"create"}, Raw: map[string]any{"role": "roles/x"}}),
		rules, model.Class{Name: "safe"})
	if got.Class.Name != "iam" {
		t.Fatalf("class = %q, want iam", got.Class.Name)
	}
	if got.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no project values", got.Attributes)
	}
}
