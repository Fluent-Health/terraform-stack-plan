package statemoves

import "testing"

func TestLoad_valid(t *testing.T) {
	m, err := Load("testdata/moves.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const stack = "stacks/prod/pipelines/content-library/fh-prod-svc"
	if !m.Targets(stack)["module.content_library"] {
		t.Errorf("expected module.content_library to be a move-target for %q", stack)
	}
	if n := m.Targets("no/such/stack").Len(); n != 0 {
		t.Errorf("unknown stack: want 0 targets, got %d", n)
	}
}

func TestSet_Covers(t *testing.T) {
	// A whole-module move-target (the granularity state-mover emits) must cover
	// the planned child resources terraform actually shows, but not a sibling
	// module that merely shares a name prefix, nor an unrelated address.
	s := Set{"module.content_library": true, "module.standalone.google_project_iam_member.x": true}
	cases := []struct {
		addr string
		want bool
	}{
		{"module.content_library", true},                                                      // exact
		{"module.content_library.google_project_iam_member.editor", true},                     // child of the module
		{"module.content_library.google_storage_bucket.b", true},                              // any child
		{"module.standalone.google_project_iam_member.x", true},                               // exact resource-level target
		{`module.standalone.google_project_iam_member.x["roles/secretmanager.viewer"]`, true}, // for_each INSTANCE of a resource-level target
		{"module.standalone.google_project_iam_member.x_other", false},                        // sibling resource, no false match
		{"module.content_library_other.google_x.y", false},                                    // sibling module, ".": no false match
		{"module.other.google_project_iam_member.z", false},                                   // unrelated
	}
	for _, c := range cases {
		if got := s.Covers(c.addr); got != c.want {
			t.Errorf("Covers(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestLoad_emptyPath_isNoop(t *testing.T) {
	m, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if n := m.Targets("anything").Len(); n != 0 {
		t.Errorf("empty path: want 0 targets, got %d", n)
	}
}
