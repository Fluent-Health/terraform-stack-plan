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

func TestLoad_emptyPath_isNoop(t *testing.T) {
	m, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if n := m.Targets("anything").Len(); n != 0 {
		t.Errorf("empty path: want 0 targets, got %d", n)
	}
}
