package codes

import (
	"regexp"
	"testing"
)

func TestCodesAreUniqueAndWellFormed(t *testing.T) {
	format := regexp.MustCompile(`^[A-Z]+-[0-9]{3}$`)
	seen := map[Code]bool{}
	for _, c := range All() {
		if !format.MatchString(string(c)) {
			t.Errorf("code %q does not match NAMESPACE-### format", c)
		}
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
	if len(All()) == 0 {
		t.Fatal("registry is empty")
	}
}
