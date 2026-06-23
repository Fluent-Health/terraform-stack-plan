package model

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/domain"
)

func TestModelTypesAreDomainAliases(t *testing.T) {
	var c Counts = domain.Counts{Change: 1}
	var _ domain.Counts = c // compiles only if alias

	var cl Class = domain.Category{Name: "iam", Icon: "🔐"}
	if cl.Label() != "🔐 iam" {
		t.Fatalf("Label via alias = %q", cl.Label())
	}
	if !c.AnyChange() {
		t.Fatal("AnyChange via alias = false")
	}
}
