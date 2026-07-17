package classify

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/moveset"
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
	got := Classify(s, rules, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 categories, got %d: %+v", len(got), got)
	}
	if got[0].Name != "iam" || got[1].Name != "destructive" {
		t.Fatalf("category order = [%q %q], want [iam destructive]", got[0].Name, got[1].Name)
	}
}

func TestNoMatchYieldsEmpty(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Action: model.ActionAdd, Actions: []string{"create"}}), rules, nil)
	if len(got) != 0 {
		t.Fatalf("no match should yield empty slice, got %+v", got)
	}
}

func TestActionsAndMinCount(t *testing.T) {
	rules := []Rule{{Name: "destructive", Actions: []string{"delete"}, MinCount: 2}}
	if cats := Classify(stack(plan.RawChange{Type: "x", Action: model.ActionDestroy, Actions: []string{"delete"}}), rules, nil); len(cats) != 0 {
		t.Fatalf("one delete must not meet min_count 2, got %+v", cats)
	}
	two := stack(
		plan.RawChange{Type: "x", Action: model.ActionDestroy, Actions: []string{"delete"}},
		plan.RawChange{Type: "y", Action: model.ActionDestroy, Actions: []string{"delete"}},
	)
	if _, ok := find(Classify(two, rules, nil), "destructive"); !ok {
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
	iam, ok := find(Classify(s, rules, nil), "iam")
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
	iam, _ := find(Classify(s, rules, nil), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1", "p2"}) {
		t.Fatalf("project = %v, want [p1 p2]", iam.Attributes["project"])
	}
}

func TestEmitAttributesNilWhenNoneConfigured(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	iam, _ := find(Classify(
		stack(plan.RawChange{Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p1"}}),
		rules, nil), "iam")
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
		rules, nil), "iam")
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
	iam, _ := find(Classify(s, rules, nil), "iam")
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
		rules, nil)
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
		if got := Classify(stack(c.ch), rules, nil); len(got) != 0 {
			t.Errorf("%s: pure state-op must not classify, got %+v", c.name, got)
		}
	}
}

// TestClassify_skipsMoveTargets: a planned create that is a pending cross-state
// move-target is skipped, so the iam rule fires only from the sibling real create.
func TestClassify_skipsMoveTargets(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐",
		TypePattern:    regexp.MustCompile("^google_project_iam_member$"),
		MinCount:       1,
		EmitAttributes: []string{"project"},
	}}
	s := stack(
		plan.RawChange{Address: "module.cl.google_project_iam_member.x", Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p-move"}},
		plan.RawChange{Address: "module.other.google_project_iam_member.y", Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p-real"}},
	)
	moveTargets := moveset.Set{"module.cl.google_project_iam_member.x": true}
	iam, ok := find(Classify(s, rules, moveTargets), "iam")
	if !ok {
		t.Fatal("expected iam category from the non-move create")
	}
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p-real"}) {
		t.Fatalf("project = %v, want [p-real] (move-target's p-move must NOT appear)", iam.Attributes["project"])
	}
}

// TestClassify_allMoveTargets_noCategory: when the only create is a move-target,
// nothing mutates and no category fires.
func TestClassify_allMoveTargets_noCategory(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐",
		TypePattern: regexp.MustCompile("^google_project_iam_member$"),
		MinCount:    1,
	}}
	s := stack(
		plan.RawChange{Address: "module.cl.google_project_iam_member.x", Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p-move"}},
	)
	moveTargets := moveset.Set{"module.cl.google_project_iam_member.x": true}
	if got := Classify(s, rules, moveTargets); len(got) != 0 {
		t.Fatalf("a move-only stack must classify to nothing, got %+v", got)
	}
}

// TestClassify_skipsModuleLevelMoveTarget: state-mover emits a whole-MODULE
// target (a `terraform state mv module.x module.y` moves the module), but the
// destination plan shows the module's CHILD resources as creates. A module-level
// target must cover those children so a moved-in pipeline stack stays non-iam —
// the real content-library pilot shape.
func TestClassify_skipsModuleLevelMoveTarget(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐",
		TypePattern: regexp.MustCompile("^google_project_iam_member$"),
		MinCount:    1,
	}}
	s := stack(
		plan.RawChange{Address: "module.content_library.google_project_iam_member.editor", Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p-move"}},
		plan.RawChange{Address: "module.content_library.google_project_iam_member.viewer", Type: "google_project_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "p-move"}},
	)
	moveTargets := moveset.Set{"module.content_library": true} // module-level
	if got := Classify(s, rules, moveTargets); len(got) != 0 {
		t.Fatalf("a module-level move-target must cover its child creates → no iam, got %+v", got)
	}
}

func TestClassify_skipsPreviousAddressMatches(t *testing.T) {
	// A planned create / destroy that has PreviousAddress matching an unindexed
	// move target (e.g. because of a same-plan moved{} block module.agent -> module.agent[0])
	// must be skipped during classification.
	rules := []Rule{{
		Name: "iam", Icon: "🔐",
		TypePattern: regexp.MustCompile("^google_project_iam_member$"),
		MinCount:    1,
	}}
	s := stack(
		plan.RawChange{
			Address:         "module.agent[0].google_project_iam_member.x",
			Type:            "google_project_iam_member",
			Action:          model.ActionAdd,
			Actions:         []string{"create"},
			PreviousAddress: "module.agent.google_project_iam_member.x",
		},
	)
	moveTargets := moveset.Set{"module.agent.google_project_iam_member.x": true}
	if got := Classify(s, rules, moveTargets); len(got) != 0 {
		t.Fatalf("a move-target matched via PreviousAddress must be skipped, got %+v", got)
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
	if _, ok := find(Classify(s, rules, nil), "iam"); !ok {
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
	iam, ok := find(Classify(s, rules, nil), "iam")
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
	iam, _ := find(Classify(s, rules, nil), "iam")
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
	iam, _ := find(Classify(s, rules, nil), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1"}) {
		t.Fatalf("project = %v, want [p1] (no override)", iam.Attributes["project"])
	}
}

// buildCacheDerivation recovers the project per-resource from a
// "<project>-build-cache" bucket name — the case stack-project fallback cannot
// handle when the stack spans multiple projects (each member is in its own).
func buildCacheDerivation() Derivation {
	return Derivation{
		Attribute:     "project",
		TypePattern:   regexp.MustCompile(`^google_storage_(bucket|managed_folder)_iam_`),
		FromAttribute: "bucket",
		Pattern:       regexp.MustCompile(`^(?P<value>.+)-build-cache$`),
	}
}

// TestProjectDerivationFromAttributePerResource is the build_cache fix: three
// projectless managed-folder members in distinct projects, with NO sibling
// carrying a project. Per-resource derivation must surface all three (the
// stack-project fallback would yield "" — ambiguous/none — and fail closed).
func TestProjectDerivationFromAttributePerResource(t *testing.T) {
	rules := []Rule{{
		Name: "iam", Icon: "🔐", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
		Derivations:    []Derivation{buildCacheDerivation()},
	}}
	s := stack(
		plan.RawChange{Type: "google_storage_managed_folder_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"bucket": "fh-dev-svc-build-cache"}},
		plan.RawChange{Type: "google_storage_managed_folder_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"bucket": "fh-test-svc-build-cache"}},
		plan.RawChange{Type: "google_storage_managed_folder_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"bucket": "fh-stage-svc-build-cache"}},
	)
	iam, ok := find(Classify(s, rules, nil), "iam")
	if !ok {
		t.Fatal("expected iam category")
	}
	want := []string{"fh-dev-svc", "fh-stage-svc", "fh-test-svc"}
	if !reflect.DeepEqual(iam.Attributes["project"], want) {
		t.Fatalf("project = %v, want %v", iam.Attributes["project"], want)
	}
}

// TestProjectDerivationDoesNotOverrideExplicit: an explicit project on the
// change wins; the derivable bucket is never consulted for that change.
func TestProjectDerivationDoesNotOverrideExplicit(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
		Derivations:    []Derivation{buildCacheDerivation()},
	}}
	s := stack(plan.RawChange{
		Type: "google_storage_bucket_iam_member", Action: model.ActionAdd, Actions: []string{"create"},
		Raw: map[string]any{"project": "p1", "bucket": "p2-build-cache"},
	})
	iam, _ := find(Classify(s, rules, nil), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"p1"}) {
		t.Fatalf("project = %v, want [p1] (explicit wins; bucket-derived p2 absent)", iam.Attributes["project"])
	}
}

// TestProjectDerivationRespectsTypePattern: a derivation scoped by
// resource_type_pattern does not apply to non-matching resource types.
func TestProjectDerivationRespectsTypePattern(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
		Derivations: []Derivation{{
			Attribute:     "project",
			TypePattern:   regexp.MustCompile(`^google_storage_managed_folder_iam_`),
			FromAttribute: "bucket",
			Pattern:       regexp.MustCompile(`^(?P<value>.+)-build-cache$`),
		}},
	}}
	// bucket member matches the iam rule but NOT the derivation's narrower type pattern.
	s := stack(plan.RawChange{
		Type: "google_storage_bucket_iam_member", Action: model.ActionAdd, Actions: []string{"create"},
		Raw: map[string]any{"bucket": "fh-dev-svc-build-cache"},
	})
	iam, _ := find(Classify(s, rules, nil), "iam")
	if v := iam.Attributes["project"]; len(v) != 0 {
		t.Fatalf("project = %v, want empty (derivation type pattern excludes this resource)", v)
	}
}

// TestProjectDerivationFallsBackToStackProjectWhenUnmatched: when the
// derivation's pattern does not match, the stack-project fallback still fills
// project from a single sibling — derivation precedes, fallback backstops.
func TestProjectDerivationFallsBackToStackProjectWhenUnmatched(t *testing.T) {
	rules := []Rule{{
		Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1,
		EmitAttributes: []string{"project"},
		Derivations:    []Derivation{buildCacheDerivation()},
	}}
	s := stack(
		plan.RawChange{Type: "google_storage_bucket_iam_member", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"bucket": "fh-dev-svc-cms"}},
		plan.RawChange{Type: "google_storage_transfer_job", Action: model.ActionAdd, Actions: []string{"create"}, Raw: map[string]any{"project": "fh-dev-svc"}},
	)
	iam, _ := find(Classify(s, rules, nil), "iam")
	if !reflect.DeepEqual(iam.Attributes["project"], []string{"fh-dev-svc"}) {
		t.Fatalf("project = %v, want [fh-dev-svc] (stack fallback when derivation unmatched)", iam.Attributes["project"])
	}
}
