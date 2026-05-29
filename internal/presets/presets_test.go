package presets

import "testing"

func TestIAMMatchesAcrossProviders(t *testing.T) {
	r, ok := Get("iam", "")
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
	r, _ := Get("iam", "⚠️")
	if r.Icon != "⚠️" {
		t.Fatalf("icon override = %q, want ⚠️", r.Icon)
	}
}

func TestUnknownPreset(t *testing.T) {
	if _, ok := Get("nope", ""); ok {
		t.Fatal("unknown preset should return ok=false")
	}
}
