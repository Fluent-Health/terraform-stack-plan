package manifest

import "testing"

func TestLoadYAML(t *testing.T) {
	m, err := Load("testdata/plan.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Terraform plan — nonprod" || m.Marker != "tfstackplan:nonprod" {
		t.Fatalf("bad header: %+v", m)
	}
	if len(m.Stacks) != 2 || m.Stacks[0].Name != "platform/nonprod" ||
		m.Stacks[1].Plan != "./out/app-dev/plan.json" {
		t.Fatalf("bad stacks: %+v", m.Stacks)
	}
}

func TestParseStackFlags(t *testing.T) {
	refs, err := ParseStackFlags([]string{
		"platform/nonprod:./out/a/plan.json",
		"svc:/abs/path/plan.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].Name != "platform/nonprod" || refs[0].Plan != "./out/a/plan.json" {
		t.Fatalf("bad ref 0: %+v", refs[0])
	}
	if refs[1].Name != "svc" || refs[1].Plan != "/abs/path/plan.json" {
		t.Fatalf("bad ref 1: %+v", refs[1])
	}
}

func TestParseStackFlagInvalid(t *testing.T) {
	if _, err := ParseStackFlags([]string{"noseparator"}); err == nil {
		t.Fatal("expected error for missing ':' separator")
	}
}
