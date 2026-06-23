package events

import (
	"encoding/json"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/domain"
)

// Counts and Category are aliases of the canonical domain types — assignable
// without conversion, and wire-identical.
func TestEventsTypesAreDomainAliases(t *testing.T) {
	var c Counts = domain.Counts{Add: 3}
	var d domain.Counts = c // compiles only if alias, not a distinct named type
	if d.Add != 3 {
		t.Fatal("alias round-trip lost data")
	}
	var cat Category = domain.Category{Name: "iam"}
	_ = cat

	// Wire shape unchanged: a typical stack's counts marshal as before.
	b, _ := json.Marshal(Counts{Add: 2, Change: 1})
	if string(b) != `{"add":2,"change":1}` {
		t.Fatalf("Counts wire = %s", b)
	}
	bc, _ := json.Marshal(Category{Name: "iam", Icon: "🔐"})
	if string(bc) != `{"name":"iam","icon":"🔐"}` {
		t.Fatalf("Category wire = %s", bc)
	}
}
