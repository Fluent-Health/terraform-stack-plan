package codes

import (
	"errors"
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

func TestErrorCarriesCode(t *testing.T) {
	err := Errorf(UnknownStatus, "unknown stack status %q", "bogus")
	if err == nil {
		t.Fatal("Errorf returned nil")
	}
	if got := err.Error(); got != `WIRE-001: unknown stack status "bogus"` {
		t.Fatalf("Error() = %q", got)
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatal("errors.As(*codes.Error) = false")
	}
	if ce.Code() != UnknownStatus {
		t.Fatalf("Code() = %q, want %q", ce.Code(), UnknownStatus)
	}
}

func TestUnknownStatusRegistered(t *testing.T) {
	for _, c := range All() {
		if c == UnknownStatus {
			return
		}
	}
	t.Fatal("UnknownStatus not in All()")
}
