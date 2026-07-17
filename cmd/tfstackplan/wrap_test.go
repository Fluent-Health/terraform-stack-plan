package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/ansi"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func ptmxAvailable() bool {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		return false
	}
	return true
}

func TestRunStepTTYMakesCommandSeeATTY(t *testing.T) {
	if !ptmxAvailable() {
		t.Skip("no /dev/ptmx")
	}
	os.Unsetenv("TFSTACKPLAN_SERVER")
	os.Unsetenv("TFSTACKPLAN_EXECUTION")
	// Capture this process's stdout to inspect the passthrough.
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	code := runWrap([]string{"--stack", "s", "--tty", "--", "sh", "-c", "test -t 1 && echo ISTTY || echo PIPE"})
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "ISTTY") {
		t.Errorf("under --tty the command did not see a TTY: %q", buf.String())
	}
}

func TestRunStepTTYExitCodePropagates(t *testing.T) {
	if !ptmxAvailable() {
		t.Skip("no /dev/ptmx")
	}
	os.Unsetenv("TFSTACKPLAN_SERVER")
	if code := runWrap([]string{"--stack", "s", "--tty", "--", "sh", "-c", "exit 5"}); code != 5 {
		t.Fatalf("exit = %d, want 5", code)
	}
}

func TestClassifyStep(t *testing.T) {
	const noChange = "...\nApply complete! Resources: 0 added, 0 changed, 0 destroyed.\n"
	const changed = "...\nApply complete! Resources: 0 added, 20 changed, 0 destroyed.\n"
	const planOut = "...\nPlan: 0 to add, 2 to change, 0 to destroy.\nSaved the plan to: plan.bin\n"

	cases := []struct {
		name      string
		exit      int
		output    string
		onSuccess events.Status
		want      events.Status
	}{
		{"apply no changes", 0, noChange, events.StatusSafe, events.StatusNochange},
		{"apply with changes", 0, changed, events.StatusSafe, events.StatusSafe},
		{"plan success not split", 0, planOut, events.StatusPlanned, events.StatusPlanned},
		{"failure", 1, changed, events.StatusSafe, events.StatusFailed},
		{"intermediate success (init)", 0, "Terraform has been successfully initialized!", "", events.Status("")},
		{"apply summary without onSuccess defaults safe", 0, changed, "", events.StatusSafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyOutcome(c.exit, c.output, c.onSuccess); got != c.want {
				t.Errorf("classifyOutcome(%d, …, %q) = %q, want %q", c.exit, c.onSuccess, got, c.want)
			}
		})
	}
}

func TestClassifyStepColoredApplyComplete(t *testing.T) {
	colored := "\x1b[0m\x1b[1m\x1b[32mApply complete! Resources: 0 added, 0 changed, 0 destroyed.\x1b[0m\n"
	if got := classifyOutcome(0, ansi.Strip(colored), events.StatusSafe); got != events.StatusNochange {
		t.Errorf("colored no-op apply classified %q, want nochange", got)
	}
	coloredChg := "\x1b[32mApply complete! Resources: 0 added, 20 changed, 0 destroyed.\x1b[0m\n"
	if got := classifyOutcome(0, ansi.Strip(coloredChg), events.StatusSafe); got != events.StatusSafe {
		t.Errorf("colored changed apply classified %q, want safe", got)
	}
}

func TestLogStreamer(t *testing.T) {
	var got []string
	s := newLogStreamer(func(chunk string) { got = append(got, chunk) })

	// One small write: buffered, not yet flushed.
	if _, err := s.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("small write flushed early: %v", got)
	}
	// Close flushes the remainder.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "hello " {
		t.Fatalf("after close got %v, want [\"hello \"]", got)
	}
}

func TestRunStepPassthroughExitCode(t *testing.T) {
	// No server configured ⇒ pure passthrough; exit code must propagate.
	os.Unsetenv("TFSTACKPLAN_SERVER")
	os.Unsetenv("TFSTACKPLAN_EXECUTION")
	if code := runWrap([]string{"--stack", "s", "--", "sh", "-c", "exit 7"}); code != 7 {
		t.Fatalf("runWrap exit = %d, want 7", code)
	}
	if code := runWrap([]string{"--stack", "s", "--", "sh", "-c", "exit 0"}); code != 0 {
		t.Fatalf("runWrap exit = %d, want 0", code)
	}
}

func TestRunStepRequiresSeparator(t *testing.T) {
	if code := runWrap([]string{"--stack", "s"}); code != 2 {
		t.Fatalf("runWrap with no command = %d, want 2", code)
	}
}

func TestLogStreamerThresholdFlush(t *testing.T) {
	var got []string
	s := newLogStreamer(func(chunk string) { got = append(got, chunk) })
	big := make([]byte, 5000) // > 4KB threshold
	for i := range big {
		big[i] = 'x'
	}
	if _, err := s.Write(big); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("threshold write did not flush")
	}
	_ = s.Close()
	var total int
	for _, c := range got {
		total += len(c)
	}
	if total != 5000 {
		t.Fatalf("streamed %d bytes, want 5000", total)
	}
}

func TestRunStepRunningFlagValidation(t *testing.T) {
	// Unknown --running status is a flag misuse → exit 2.
	if code := runWrap([]string{"--stack", "a", "--running", "bogus", "--", "true"}); code != 2 {
		t.Fatalf("run step --running bogus = %d, want 2", code)
	}
}

func TestRunStepAliasWarnsAndDelegates(t *testing.T) {
	// `run step` must keep working (downstream terramate scripts still call it)
	// but print a one-line deprecation warning pointing at `run wrap`. Drive it
	// through the real `run` dispatcher (runRun), the same path a
	// `tfstackplan run step ...` invocation takes.
	os.Unsetenv("TFSTACKPLAN_SERVER")
	os.Unsetenv("TFSTACKPLAN_EXECUTION")

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := runRun([]string{"step", "--stack", "s", "--", "true"})

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)

	if code != 0 {
		t.Fatalf("run step alias exit = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "run step is deprecated") ||
		!strings.Contains(buf.String(), "run wrap") {
		t.Errorf("run step did not emit the deprecation warning; stderr = %q", buf.String())
	}
}

func TestRunWrapCanonicalVerb(t *testing.T) {
	// `run wrap` is the canonical verb — no deprecation warning.
	os.Unsetenv("TFSTACKPLAN_SERVER")
	os.Unsetenv("TFSTACKPLAN_EXECUTION")

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	code := runRun([]string{"wrap", "--stack", "s", "--", "true"})

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	io.Copy(&buf, r)

	if code != 0 {
		t.Fatalf("run wrap exit = %d, want 0", code)
	}
	if strings.Contains(buf.String(), "deprecated") {
		t.Errorf("run wrap should not warn; stderr = %q", buf.String())
	}
}
