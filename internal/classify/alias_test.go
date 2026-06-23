package classify

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/domain"
)

func TestClassifyCategoryIsDomainAlias(t *testing.T) {
	var c Category = domain.Category{Name: "iam", Attributes: map[string][]string{"project": {"p1"}}}
	var _ domain.Category = c // compiles only if alias
	if c.Attributes["project"][0] != "p1" {
		t.Fatal("attributes lost")
	}
}
