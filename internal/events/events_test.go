package events

import (
	"encoding/json"
	"testing"
)

func TestVersionPresent(t *testing.T) {
	if Version < 1 {
		t.Fatalf("Version = %d, want >= 1", Version)
	}
}

func TestInitJSONFieldNames(t *testing.T) {
	in := Init{
		ID:          "exec-1",
		Repo:        "owner/repo",
		SHA:         "abc123",
		PR:          42,
		Environment: "staging",
		LogURL:      "https://ci/log",
		Context:     "iam/staging",
		Stacks:      []StackState{{Path: "stacks/a", Project: "proj-a", Status: StatusPending}},
		Edges:       []Edge{{From: "stacks/a", To: "stacks/b"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`"environment":"staging"`, `"pr":42`, `"log_url":"https://ci/log"`,
		`"path":"stacks/a"`, `"project":"proj-a"`, `"status":"pending"`,
		`"from":"stacks/a"`, `"to":"stacks/b"`,
	} {
		if !contains(got, want) {
			t.Errorf("Init JSON missing %s\n got: %s", want, got)
		}
	}
	// Must NOT carry the old Fluent-specific key.
	if contains(got, `"tier"`) {
		t.Errorf("Init JSON must not contain legacy \"tier\" key: %s", got)
	}
}

func TestFinalizeGatesRoundTrip(t *testing.T) {
	f := Finalize{
		ID:             "exec-1",
		ReportMarkdown: "# report",
		Projects:       map[string]string{"stacks/a": "proj-a"},
		Gates:          []GateTarget{{Class: "iam", Target: "proj-a"}},
		Moving:         []string{"stacks/c"},
		Failed:         false,
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var got Finalize
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Gates) != 1 || got.Gates[0].Class != "iam" || got.Gates[0].Target != "proj-a" {
		t.Fatalf("gates round-trip = %+v", got.Gates)
	}
	if got.Projects["stacks/a"] != "proj-a" {
		t.Fatalf("projects round-trip = %+v", got.Projects)
	}
}

func TestLogChunkRoundTrip(t *testing.T) {
	c := LogChunk{ID: "e1", Stack: "stacks/a", Data: "hello\n"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var got LogChunk
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Errorf("round-trip = %+v, want %+v", got, c)
	}
	if !contains(string(b), `"stack":"stacks/a"`) || !contains(string(b), `"data":"hello\n"`) {
		t.Errorf("json = %s", b)
	}
}

func TestCountsOmittedWhenNil(t *testing.T) {
	b, err := json.Marshal(StackState{Path: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"path":"a"}` {
		t.Fatalf("nil counts must be omitted, got %s", got)
	}
}

func TestCountsRoundTrip(t *testing.T) {
	in := StackState{Path: "a", Counts: &Counts{Add: 6, Change: 2, Replace: 1, Move: 1}}
	b, _ := json.Marshal(in)
	var out StackState
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Counts == nil || out.Counts.Add != 6 || out.Counts.Replace != 1 || out.Counts.Move != 1 {
		t.Fatalf("round-trip lost counts: %+v", out.Counts)
	}
}

func TestFinalizeCountsMap(t *testing.T) {
	f := Finalize{ID: "e1", Counts: map[string]Counts{"a": {Destroy: 2}}}
	b, _ := json.Marshal(f)
	var out Finalize
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Counts["a"].Destroy != 2 {
		t.Fatalf("finalize counts map lost: %+v", out.Counts)
	}
}

func TestStatusValidAndParse(t *testing.T) {
	if !StatusPending.Valid() {
		t.Error("pending should be a valid status")
	}
	if Status("invalid").Valid() {
		t.Error("invalid should not be a valid status")
	}
	if !Status("").Valid() {
		t.Error("empty status should be valid for decoding")
	}

	got, err := ParseStatus("gated")
	if err != nil || got != StatusGated {
		t.Errorf("ParseStatus(gated) = (%q, %v), want (gated, nil)", got, err)
	}

	_, err = ParseStatus("bogus")
	if err == nil {
		t.Error("ParseStatus(bogus) expected error, got nil")
	}

	var status Status
	if err := json.Unmarshal([]byte(`"running"`), &status); err != nil || status != StatusRunning {
		t.Errorf("Unmarshal(running) failed: %v", err)
	}
}

func TestPhaseTickingAndValid(t *testing.T) {
	if !PhasePlanning.Ticking() {
		t.Error("planning should be ticking")
	}
	if PhaseWarming.Ticking() {
		t.Error("warming should not be ticking")
	}

	if !PhaseVerifying.Valid() {
		t.Error("verifying should be a valid phase")
	}
	if Phase("invalid").Valid() {
		t.Error("invalid should not be a valid phase")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
