package source

import "testing"

func TestIndexRootAndLocalModule(t *testing.T) {
	idx := Build("testdata/stack", "testdata")

	loc, ok := idx.Lookup("", "google_storage_bucket", "state")
	if !ok {
		t.Fatal("root resource not found")
	}
	if loc.File != "stack/main.tf" || loc.Line != 1 {
		t.Errorf("root loc = %+v, want stack/main.tf:1", loc)
	}
	if loc, ok := idx.Lookup("", "google_project_iam_member", "editor"); !ok || loc.Line != 5 {
		t.Errorf("second root resource loc = %+v ok=%v", loc, ok)
	}
	if loc, ok := idx.Lookup("module.net", "google_compute_firewall", "web"); !ok || loc.File != "stack/modules/net/net.tf" {
		t.Errorf("module resource loc = %+v ok=%v", loc, ok)
	}
	if _, ok := idx.Lookup("", "google_storage_bucket", "missing"); ok {
		t.Error("expected miss for unknown resource")
	}
	if _, ok := idx.Lookup("module.remote", "anything", "x"); ok {
		t.Error("remote module resources must not be indexed")
	}
}

func TestModuleKey(t *testing.T) {
	cases := map[string]string{"": "", "module.a": "a", "module.a.module.b": "a.b"}
	for in, want := range cases {
		if got := moduleKey(in); got != want {
			t.Errorf("moduleKey(%q) = %q, want %q", in, got, want)
		}
	}
}
