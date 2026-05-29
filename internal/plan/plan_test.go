package plan

import (
	"os"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

func load(t *testing.T, name string) RawStack {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Parse("stack", data)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestParseCountsAndAttrs(t *testing.T) {
	rs := load(t, "update.json")
	if rs.Counts != (model.Counts{Change: 1}) {
		t.Fatalf("counts = %+v, want Change:1", rs.Counts)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(rs.Changes))
	}
	c := rs.Changes[0]
	if c.Action != model.ActionChange || c.Type != "google_project_iam_member" {
		t.Fatalf("bad change: %+v", c)
	}
	if len(c.Attrs) != 1 || c.Attrs[0].Name != "role" {
		t.Fatalf("expected one changed attr 'role', got %+v", c.Attrs)
	}
}

func TestParseMixedActions(t *testing.T) {
	rs := load(t, "mixed.json")
	want := model.Counts{Add: 1, Destroy: 1, Replace: 1}
	if rs.Counts != want {
		t.Fatalf("counts = %+v, want %+v", rs.Counts, want)
	}
	if len(rs.Changes) != 3 {
		t.Fatalf("got %d changes (no-op should be excluded), want 3", len(rs.Changes))
	}
	for _, c := range rs.Changes {
		switch c.Action {
		case model.ActionAdd:
			// create should expose after-attrs
			if len(c.Attrs) == 0 {
				t.Fatalf("create should have attrs, got none for %s", c.Address)
			}
			if c.Attrs[0].Name != "name" || c.Attrs[0].After != "b" {
				t.Fatalf("create attr mismatch: %+v", c.Attrs)
			}
		case model.ActionDestroy:
			// delete should expose before-attrs
			if len(c.Attrs) == 0 {
				t.Fatalf("delete should have attrs, got none for %s", c.Address)
			}
			if c.Attrs[0].Name != "name" || c.Attrs[0].Before != "b" {
				t.Fatalf("delete attr mismatch: %+v", c.Attrs)
			}
		}
	}
}

func TestForgetActionExcluded(t *testing.T) {
	rs := load(t, "forget.json")
	// forget resource must be excluded; only the update should remain
	if rs.Counts != (model.Counts{Change: 1}) {
		t.Fatalf("counts = %+v, want Change:1 (forget excluded)", rs.Counts)
	}
	if len(rs.Changes) != 1 {
		t.Fatalf("want 1 change (forget excluded), got %d", len(rs.Changes))
	}
	if rs.Changes[0].Address != "a.update" {
		t.Fatalf("expected a.update, got %s", rs.Changes[0].Address)
	}
}

func TestSensitiveAndUnknownAndPartialChange(t *testing.T) {
	rs := load(t, "sensitive.json")
	if len(rs.Changes) != 1 {
		t.Fatalf("want 1 change, got %d", len(rs.Changes))
	}
	attrs := rs.Changes[0].Attrs
	byName := map[string]RawAttr{}
	for _, a := range attrs {
		byName[a.Name] = a
	}
	// "unchanged" is identical before/after and not unknown → must be excluded
	if _, ok := byName["unchanged"]; ok {
		t.Fatal("unchanged attribute should not be included")
	}
	// "secret_data" changed and is sensitive
	sd, ok := byName["secret_data"]
	if !ok || !sd.Sensitive {
		t.Fatalf("secret_data should be present and Sensitive, got %+v", sd)
	}
	// "computed_id" is unchanged value but marked unknown → must be included with Unknown=true
	ci, ok := byName["computed_id"]
	if !ok || !ci.Unknown {
		t.Fatalf("computed_id should be present and Unknown, got %+v", ci)
	}
}

func TestParseCreateExtractsAfterAttrs(t *testing.T) {
	rs := load(t, "create.json")
	if rs.Counts.Add != 1 {
		t.Fatalf("Add count = %d, want 1", rs.Counts.Add)
	}
	got := map[string]RawAttr{}
	for _, a := range rs.Changes[0].Attrs {
		got[a.Name] = a
	}
	if got["account_id"].After != "app-api" {
		t.Errorf("account_id After = %v, want app-api", got["account_id"].After)
	}
	if !got["unique_id"].Unknown {
		t.Errorf("unique_id should be known-after-apply")
	}
}
