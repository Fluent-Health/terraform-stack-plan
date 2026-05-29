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

func TestForgetActionSurfaced(t *testing.T) {
	rs := load(t, "forget.json")
	// forget is now surfaced: the update plus the forget, with Forget counted.
	if rs.Counts != (model.Counts{Change: 1, Forget: 1}) {
		t.Fatalf("counts = %+v, want Change:1 Forget:1", rs.Counts)
	}
	if len(rs.Changes) != 2 {
		t.Fatalf("want 2 changes (update + forget), got %d", len(rs.Changes))
	}
	var fg *RawChange
	for i := range rs.Changes {
		if rs.Changes[i].Address == "a.forget" {
			fg = &rs.Changes[i]
		}
	}
	if fg == nil {
		t.Fatal("forget change missing")
	}
	if fg.Action != model.ActionForget {
		t.Fatalf("forget action = %q, want forget", fg.Action)
	}
	if len(fg.Attrs) == 0 || fg.Attrs[0].Before != "b" {
		t.Fatalf("forget should carry before-attrs, got %+v", fg.Attrs)
	}
}

func TestMovedAndImported(t *testing.T) {
	data := []byte(`{
	  "format_version": "1.2",
	  "resource_changes": [
	    {"address":"a.new","previous_address":"a.old","type":"t","name":"new",
	     "change":{"actions":["no-op"],"before":{"x":"1"},"after":{"x":"1"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"b.imported","type":"t","name":"imported",
	     "change":{"actions":["no-op"],"importing":{"id":"i-0abc"},
	       "before":{"x":"1"},"after":{"x":"1"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}},
	    {"address":"c.skip","type":"t","name":"skip",
	     "change":{"actions":["no-op"],"before":{"x":"1"},"after":{"x":"1"},
	       "after_unknown":{},"before_sensitive":{},"after_sensitive":{}}}
	  ]
	}`)
	rs, err := Parse("s", data)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Counts.Move != 1 || rs.Counts.Import != 1 {
		t.Fatalf("counts = %+v, want Move:1 Import:1", rs.Counts)
	}
	if len(rs.Changes) != 2 { // plain no-op c.skip is dropped
		t.Fatalf("want 2 changes (moved + imported), got %d", len(rs.Changes))
	}
	byAddr := map[string]RawChange{}
	for _, c := range rs.Changes {
		byAddr[c.Address] = c
	}
	if mv := byAddr["a.new"]; !mv.Moved || mv.PreviousAddress != "a.old" || mv.Action != model.ActionNoop {
		t.Fatalf("moved parse wrong: %+v", mv)
	}
	if im := byAddr["b.imported"]; !im.Imported || im.ImportID != "i-0abc" || im.Action != model.ActionNoop {
		t.Fatalf("imported parse wrong: %+v", im)
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
