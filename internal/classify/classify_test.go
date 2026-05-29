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
	if got.Name != "iam" {
		t.Fatalf("update to iam resource should classify iam, got %q", got.Name)
	}
}

func TestActionsAndMinCount(t *testing.T) {
	rules := []Rule{{Name: "destructive", Actions: []string{"delete"}, MinCount: 2}}
	def := model.Class{Name: "safe"}

	oneDelete := stack(plan.RawChange{Type: "x", Actions: []string{"delete"}})
	if Classify(oneDelete, rules, def).Name != "safe" {
		t.Fatal("one delete should not meet min_count 2")
	}
	twoDeletes := stack(
		plan.RawChange{Type: "x", Actions: []string{"delete"}},
		plan.RawChange{Type: "y", Actions: []string{"delete"}},
	)
	if Classify(twoDeletes, rules, def).Name != "destructive" {
		t.Fatal("two deletes should meet min_count 2")
	}
}

func TestDefaultWhenNoMatch(t *testing.T) {
	rules := []Rule{{Name: "iam", TypePattern: regexp.MustCompile(`_iam_`), MinCount: 1}}
	def := model.Class{Name: "safe", Icon: "✅"}
	got := Classify(stack(plan.RawChange{Type: "google_storage_bucket", Actions: []string{"create"}}), rules, def)
	if got != def {
		t.Fatalf("no match should yield default %+v, got %+v", def, got)
	}
}
