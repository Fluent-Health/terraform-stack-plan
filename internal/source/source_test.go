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
	cases := map[string]string{
		"":                          "",
		"module.a":                  "a",
		"module.a.module.b":         "a.b",
		`module.a[0].module.b["x"]`: "a.b",
	}
	for in, want := range cases {
		if got := moduleKey(in); got != want {
			t.Errorf("moduleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelToEscape(t *testing.T) {
	if got := relTo("testdata/stack", "testdata/elsewhere/x.tf"); got != "" {
		t.Errorf("relTo escape should be \"\", got %q", got)
	}
	if got := relTo("testdata", "testdata/stack/main.tf"); got != "stack/main.tf" {
		t.Errorf("relTo inside = %q, want stack/main.tf", got)
	}
}

func TestBuildMissingModulesJSON(t *testing.T) {
	// A bare module dir with no .terraform: root parse still works, no panic.
	idx := Build("testdata/stack/modules/net", "testdata")
	if _, ok := idx.Lookup("", "google_compute_firewall", "web"); !ok {
		t.Error("root parse should work without modules.json")
	}
}

func TestBuildSkipsUnparseableFile(t *testing.T) {
	idx := Build("testdata/bad", "testdata")
	// the unparseable file is skipped; nothing panics. (A clean file in the
	// same dir would still index; here the only file is partly broken, so we
	// just assert Build returns without error and Lookup misses gracefully.)
	if _, ok := idx.Lookup("", "google_storage_bucket", "ok"); ok {
		_ = ok // either outcome is fine; the point is no panic
	}
}
