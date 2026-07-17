package classify

import "testing"

func TestIAMMatchesAcrossProviders(t *testing.T) {
	r, ok := PresetRule("iam", "", nil)
	if !ok {
		t.Fatal("iam preset should exist")
	}
	if r.Icon != "🔐" {
		t.Fatalf("default iam icon = %q, want 🔐", r.Icon)
	}
	match := []string{
		"google_project_iam_member",
		"google-beta_storage_bucket_iam_binding",
		"aws_iam_role",
		"azurerm_role_assignment",
	}
	for _, ty := range match {
		if !r.TypePattern.MatchString(ty) {
			t.Errorf("iam preset should match %q", ty)
		}
	}
	for _, ty := range []string{"google_storage_bucket", "aws_s3_bucket"} {
		if r.TypePattern.MatchString(ty) {
			t.Errorf("iam preset should NOT match %q", ty)
		}
	}
}

func TestIconOverride(t *testing.T) {
	r, _ := PresetRule("iam", "⚠️", nil)
	if r.Icon != "⚠️" {
		t.Fatalf("icon override = %q, want ⚠️", r.Icon)
	}
}

func TestUnknownPreset(t *testing.T) {
	if _, ok := PresetRule("nope", "", nil); ok {
		t.Fatal("unknown preset should return ok=false")
	}
}

func TestEmitAttributesPropagated(t *testing.T) {
	r, _ := PresetRule("iam", "", []string{"project"})
	if len(r.EmitAttributes) != 1 || r.EmitAttributes[0] != "project" {
		t.Fatalf("EmitAttributes = %v, want [project]", r.EmitAttributes)
	}
}
