package events

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/codes"
)

func TestAllKnownStatusesAreValid(t *testing.T) {
	for _, s := range AllStatuses() {
		if !s.Valid() {
			t.Errorf("AllStatuses() member %q reports !Valid()", s)
		}
	}
	if !Status("").Valid() {
		t.Error(`empty status "" should be Valid (unset/zero)`)
	}
	if Status("bogus").Valid() {
		t.Error(`"bogus" should not be Valid`)
	}
}

func TestParseStatusRejectsUnknownWithCode(t *testing.T) {
	if _, err := ParseStatus("planned"); err != nil {
		t.Fatalf("ParseStatus(planned) errored: %v", err)
	}
	_, err := ParseStatus("bogus")
	if err == nil {
		t.Fatal("ParseStatus(bogus) = nil error")
	}
	var ce *codes.Error
	if !errors.As(err, &ce) || ce.Code() != codes.UnknownStatus {
		t.Fatalf("want codes.UnknownStatus, got %v", err)
	}
}

func TestStatusUnmarshalJSON(t *testing.T) {
	// Valid status in a struct round-trips.
	var u Update
	if err := json.Unmarshal([]byte(`{"id":"x","stack":"a","status":"planned"}`), &u); err != nil {
		t.Fatalf("decode valid: %v", err)
	}
	if u.Status != StatusPlanned {
		t.Fatalf("status = %q, want planned", u.Status)
	}
	// Unknown status fails decode.
	err := json.Unmarshal([]byte(`{"id":"x","stack":"a","status":"bogus"}`), &u)
	if err == nil {
		t.Fatal("decode of unknown status = nil error")
	}
	// Empty/absent status is accepted (unset).
	var u2 Update
	if err := json.Unmarshal([]byte(`{"id":"x","stack":"a"}`), &u2); err != nil {
		t.Fatalf("decode absent status: %v", err)
	}
}
