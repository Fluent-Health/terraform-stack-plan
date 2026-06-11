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
