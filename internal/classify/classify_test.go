package classify

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
)

func stack(changes ...plan.RawChange) plan.RawStack {
	return plan.RawStack{Name: "s", Changes: changes}
}

// find returns the category with the given name, or false.
func find(cats []Category, name string) (Category, bool) {
	for _, c := range cats {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}

func TestAllMatchingRulesFire(t *testing.T) {
	rules := []Rule{
		{Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1},
		{Name: "destructive", Icon: "💣", Actions: []string{"delete"}, MinCount: 1},
	}
	// A deleted IAM member matches BOTH rules.
	s := stack(plan.RawChange{Type: "google_project_iam_member", Action: model.ActionDestroy, Actions: []string{"delete"}})
	got := Classify(s, rules)
	if len(got) != 2 {
		t.Fatalf("expected 2 categories, got %d: %+v", len(got), got)
	}
	if got[0].Name != "iam" || got[1].Name != "destructive" {
		t.Fatalf("category order = [%q %q], want [iam destructive]", got[0].Name, got[1].Name)
	}
}

func TestNoMatchYieldsEmpty(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Action: model.ActionAdd, Actions: []string{"create"}}), rules)
	if len(got) != 0 {
		t.Fatalf("no match should yield empty slice, got %+v", got)
	}
}

func TestActionsAndMinCount(t *testing.T) {
	rules := []Rule{{Name: "destructive", Actions: []string{"delete"}, MinCount: 2}}
	if cats := Classify(stack(plan.RawChange{Type: "x", Action: model.ActionDestroy, Actions: []string{"delete"}}), rules); len(cats) != 0 {
		t.Fatalf("one delete must not meet min_count 2, got %+v", cats)
	}
	two := stack(
		plan.RawChange{Type: "x", Action: model.ActionDestroy, Actions: []string{"delete"}},
		plan.RawChange{Type: "y", Action: model.ActionDestroy, Actions: []string{"delete"}},
	)
	if _, ok := find(Classify(two, rules), "destructive"); !ok {
		t.Fatal("two deletes should meet min_count 2")
	}
}

func TestEmitAttributesFromMatchedChangesOnly(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionChange, Actions: []string{"update"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_storage_bucket", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	iam, ok := find(Classify(s, rules), "iam")
	if !ok {
		t.Fatal("expected iam category")
	}
	if len(iam.Attributes["project"]) != 1 || iam.Attributes["project"][0] != "p1" {
		t.Fatalf("project = %v, want [p1] (bucket's p2 must NOT appear)", iam.Attributes["project"])
	}
}

func TestEmitAttributesDedupeAndSort(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	iam, _ := find(Classify(s, rules), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1", "p2"}) {
		t.Fatalf("project = %v, want [p1 p2]", iam.Attributes["project"])
	}
}

func TestEmitAttributesNilWhenNoneConfigured(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	iam, _ := find(Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules), "iam")
	if iam.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no emit_attributes", iam.Attributes)
	}
}

func TestEmitAttributesNilWhenNoValuesFound(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	iam, ok := find(Classify(
		stack(plan.RawChange{Type: "google_organization_iam_binding", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"role": "roles/x"}}),
		rules), "iam")
	if !ok {
		t.Fatal("expected iam category")
	}
	if iam.Attributes != nil {
		t.Fatalf("Attributes = %v, want nil when no project values", iam.Attributes)
	}
}

func TestEmitMultipleAttributes(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project", "role"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1", "role": "roles/viewer"}},
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p2", "role": "roles/viewer"}},
	)
	iam, _ := find(Classify(s, rules), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1", "p2"}) {
		t.Errorf("project = %v, want [p1 p2]", iam.Attributes["project"])
	}
	if !reflect.DeepEqual(iam.Attributes["role"], []string{"roles/viewer"}) {
		t.Errorf("role = %v, want [roles/viewer]", iam.Attributes["role"])
	}
}

func TestBelowMinCountDoesNotFire(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 2,
		EmitAttributes: []string{"project"},
	}}
	got := Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules)
	if len(got) != 0 {
		t.Fatalf("rule below MinCount must not fire, got %+v", got)
	}
}

// TestStateOpsDoNotClassify pins the core behaviour: a pure move / import /
// forget of an IAM resource needs no apply-time write permission, so it must
// NOT contribute the iam category.
func TestStateOpsDoNotClassify(t *testing.T) {
	rules := []Rule{{Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	cases := []struct {
		name string
		ch   plan.RawChange
	}{
		{"move-only", plan.RawChange{Type: "google_project_iam_member", Action: model.ActionNoop, Actions: []string{"no-op"}, Moved: true, PreviousAddress: "google_project_iam_member.old"}},
		{"import-only", plan.RawChange{Type: "google_project_iam_member", Action: model.ActionNoop, Actions: []string{"no-op"}, Imported: true, ImportID: "x"}},
		{"forget", plan.RawChange{Type: "google_project_iam_member", Action: model.ActionForget, Actions: []string{"forget"}}},
	}
	for _, c := range cases {
		if got := Classify(stack(c.ch), rules); len(got) != 0 {
			t.Errorf("%s: pure state-op must not classify, got %+v", c.name, got)
		}
	}
}

// TestMutatingChangeStillClassifiesWhenAlsoMoved confirms the move annotation
// does not suppress a real mutation: an updated-and-moved IAM binding still
// needs the grant, so it must classify.
func TestMutatingChangeStillClassifiesWhenAlsoMoved(t *testing.T) {
	rules := []Rule{{Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	s := stack(plan.RawChange{
		Type: "google_project_iam_member", Action: model.ActionChange, Actions: []string{"update"},
		Moved: true, PreviousAddress: "google_project_iam_member.old",
	})
	if _, ok := find(Classify(s, rules), "iam"); !ok {
		t.Fatal("update+move IAM change must still classify as iam")
	}
}

func TestSummarizeUnionsAcrossStacks(t *testing.T) {
	rules := []Rule{
		{Name: "iam", Icon: "🔐"},
		{Name: "sql-server", Icon: "🗄"},
	}
	perStack := [][]Category{
		{{Name: "iam", Icon: "🔐", Attributes: map[string][]string{"project": {"p1", "p2"}}}},
		{
			{Name: "iam", Icon: "🔐", Attributes: map[string][]string{"project": {"p2", "p3"}}},
			{Name: "sql-server", Icon: "🗄", Attributes: map[string][]string{"instance": {"db1"}}},
		},
		{}, // a stack that matched nothing contributes nothing
	}
	got := Summarize(perStack, rules)
	if len(got) != 2 {
		t.Fatalf("expected 2 summary categories, got %d: %+v", len(got), got)
	}
	if got[0].Name != "iam" || got[1].Name != "sql-server" {
		t.Fatalf("summary order = [%q %q], want [iam sql-server]", got[0].Name, got[1].Name)
	}
	if !reflect.DeepEqual(got[0].Attributes["project"], []string{"p1", "p2", "p3"}) {
		t.Fatalf("iam project union = %v, want [p1 p2 p3]", got[0].Attributes["project"])
	}
	if !reflect.DeepEqual(got[1].Attributes["instance"], []string{"db1"}) {
		t.Fatalf("sql-server instance = %v, want [db1]", got[1].Attributes["instance"])
	}
}

func TestSummarizeEmpty(t *testing.T) {
	if got := Summarize([][]Category{{}, {}}, []Rule{{Name: "iam"}}); len(got) != 0 {
		t.Fatalf("no categories should summarize to empty, got %+v", got)
	}
}

func TestProjectFallbackToStackProjectForProjectlessIAM(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	// Bucket IAM has no project; a sibling transfer job in the same stack does.
	s := stack(
		plan.RawChange{Type: "google_storage_bucket_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"bucket": "fh-dev-svc-cms", "role": "roles/storage.objectViewer"}},
		plan.RawChange{Type: "google_storage_transfer_job", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "fh-dev-svc"}},
	)
	iam, ok := find(Classify(s, rules), "iam")
	if !ok {
		t.Fatal("expected iam category")
	}
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"fh-dev-svc"}) {
		t.Fatalf("project = %v, want [fh-dev-svc] (stack-project fallback)", iam.Attributes["project"])
	}
}

func TestProjectFallbackSkippedWhenStackProjectAmbiguous(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_storage_bucket_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"bucket": "b"}},
		plan.RawChange{Type: "google_storage_transfer_job", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_pubsub_topic", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p2"}},
	)
	iam, _ := find(Classify(s, rules), "iam")
	if v := iam.Attributes["project"]; len(v) != 0 {
		t.Fatalf("project = %v, want empty (ambiguous stack project → no fallback)", v)
	}
}

func TestProjectFallbackDoesNotOverrideExplicitIAMProject(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}},
		plan.RawChange{Type: "google_storage_transfer_job", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}},
	)
	iam, _ := find(Classify(s, rules), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1"}) {
		t.Fatalf("project = %v, want [p1] (no override)", iam.Attributes["project"])
	}
}
