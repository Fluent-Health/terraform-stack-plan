package domain

import (
	"encoding/json"
	"testing"
)

func TestCountsTotalAndAnyChange(t *testing.T) {
	c := Counts{Add: 1, Change: 2, Destroy: 0, Replace: 1}
	if got := c.Total(); got != 4 {
		t.Fatalf("Total() = %d, want 4", got)
	}
	if !c.AnyChange() {
		t.Fatal("AnyChange() = false, want true")
	}
	if (Counts{}).AnyChange() {
		t.Fatal("zero Counts AnyChange() = true, want false")
	}
	if !(Counts{Move: 1}).AnyChange() {
		t.Fatal("move-only Counts AnyChange() = false, want true")
	}
}

func TestCountsJSONOmitsZero(t *testing.T) {
	b, err := json.Marshal(Counts{Add: 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"add":2}` {
		t.Fatalf("Counts JSON = %s, want {\"add\":2}", b)
	}
}

func TestCategoryLabel(t *testing.T) {
	if got := (Category{Name: "iam", Icon: "🔐"}).Label(); got != "🔐 iam" {
		t.Fatalf("Label() = %q, want \"🔐 iam\"", got)
	}
	if got := (Category{Name: "safe"}).Label(); got != "safe" {
		t.Fatalf("Label() = %q, want \"safe\"", got)
	}
}

func TestCategoryJSON(t *testing.T) {
	b, err := json.Marshal(Category{Name: "iam", Icon: "🔐", Attributes: map[string][]string{"project": {"p1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"name":"iam","icon":"🔐","attributes":{"project":["p1"]}}` {
		t.Fatalf("Category JSON = %s", b)
	}
	// icon + attributes omitted when empty
	b2, _ := json.Marshal(Category{Name: "safe"})
	if string(b2) != `{"name":"safe"}` {
		t.Fatalf("bare Category JSON = %s, want {\"name\":\"safe\"}", b2)
	}
}
