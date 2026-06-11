package statemove

import "testing"

func TestDecide(t *testing.T) {
	if d, err := decide(map[string]bool{"aws_s3_bucket.x": true}, map[string]bool{}, "aws_s3_bucket.x", "aws_s3_bucket.x"); err != nil || d != DecisionMove {
		t.Errorf("source-only => move; got %v %v", d, err)
	}
	if d, err := decide(map[string]bool{}, map[string]bool{"a.b": true}, "a.b", "a.b"); err != nil || d != DecisionSkip {
		t.Errorf("dest-only => skip; got %v %v", d, err)
	}
	if _, err := decide(map[string]bool{"a.b": true}, map[string]bool{"a.b": true}, "a.b", "a.b"); err == nil {
		t.Error("both => error (ambiguous)")
	}
	if _, err := decide(map[string]bool{}, map[string]bool{}, "a.b", "a.b"); err == nil {
		t.Error("neither => error")
	}
}
