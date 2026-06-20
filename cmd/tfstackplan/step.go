package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/ansi"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// applyCompleteRE matches terraform's apply summary line (with -no-color).
var applyCompleteRE = regexp.MustCompile(`Apply complete! Resources: (\d+) added, (\d+) changed, (\d+) destroyed`)

// classifyStep maps a wrapped command's outcome to the stack status to report.
//
//   - non-zero exit                              → failed
//   - exit 0 + apply summary with 0/0/0          → nochange
//   - exit 0 + apply summary with any non-zero    → safe (or onSuccess if set)
//   - exit 0 + no apply summary                   → onSuccess (may be "" = no terminal tick)
//
// The no-op split fires only when an apply summary is present, so onSuccess
// "planned" (a plan step) is never rewritten to nochange.
func classifyStep(exitCode int, output string, onSuccess events.Status) events.Status {
	if exitCode != 0 {
		return events.StatusFailed
	}
	if m := applyCompleteRE.FindStringSubmatch(output); m != nil {
		if m[1] == "0" && m[2] == "0" && m[3] == "0" {
			return events.StatusNochange
		}
		if onSuccess == "" {
			return events.StatusSafe
		}
	}
	return onSuccess
}

// flushThreshold is the buffer size at which logStreamer posts a chunk. Apply
// output is near-continuous, so threshold batching gives good liveness without
// a POST per line.
const flushThreshold = 4 << 10 // 4 KB

// logStreamer batches Writes and posts them as log chunks. It is an io.WriteCloser;
// Close flushes the remainder. All posting is via the injected post func (the
// seam for tests and for the no-op offline path).
type logStreamer struct {
	post func(string)
	buf  strings.Builder
}

func newLogStreamer(post func(string)) *logStreamer {
	return &logStreamer{post: post}
}

func (s *logStreamer) Write(p []byte) (int, error) {
	s.buf.Write(p)
	if s.buf.Len() >= flushThreshold {
		s.flush()
	}
	return len(p), nil
}

func (s *logStreamer) flush() {
	if s.buf.Len() == 0 {
		return
	}
	s.post(s.buf.String())
	s.buf.Reset()
}

func (s *logStreamer) Close() error {
	s.flush()
	return nil
}

// runStep wraps one stack command: it runs the command with passthrough stdio,
// and (only when a server + execution id are configured) ticks running before,
// streams output as log chunks, and ticks the terminal status after — so the
// outcome tick fires inside the same process terramate runs to completion, even
// when a parallel abort never advances to a later job command. It always exits
// with the wrapped command's exit code.
//
// Usage: tfstackplan run step --stack <path> [--on-success <status>] -- <command...>
func runStep(args []string) int {
	fs := flag.NewFlagSet("run step", flag.ContinueOnError)
	stack := fs.String("stack", "", "stack path (defaults to $"+runner.EnvStack+")")
	onSuccess := fs.String("on-success", "", "status to report on a zero exit (e.g. safe, planned); empty = intermediate, report nothing on success")
	// Parse only the flags before "--"; everything after is the wrapped command.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "tfstackplan run step: expected -- <command...>")
		return 2
	}
	if err := fs.Parse(args[:sep]); err != nil {
		return 2
	}
	cmdArgs := args[sep+1:]

	s := *stack
	if s == "" {
		s = os.Getenv(runner.EnvStack)
	}
	execID := os.Getenv(runner.EnvExecution)
	reporting := s != "" && execID != "" && runner.ClientFromEnv().Enabled()

	ctx := context.Background()
	var client *runner.Client
	var streamer *logStreamer
	// Build passthrough writer slices; start with the real stdio writers.
	stdoutWriters := []io.Writer{os.Stdout}
	stderrWriters := []io.Writer{os.Stderr}
	if reporting {
		client = runner.ClientFromEnv()
		_ = client.Update(ctx, events.Update{ID: execID, Stack: s, Status: events.StatusRunning})
		streamer = newLogStreamer(func(chunk string) {
			_ = client.LogChunk(ctx, events.LogChunk{ID: execID, Stack: s, Data: chunk})
		})
		stdoutWriters = append(stdoutWriters, streamer)
		stderrWriters = append(stderrWriters, streamer)
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	// Capture combined output for outcome classification (apply summary line).
	// Use freshly-composed slices to avoid backing-array aliasing between stdout
	// and stderr MultiWriters.
	var captured capWriter
	cmd.Stdout = io.MultiWriter(append(append([]io.Writer{}, stdoutWriters...), &captured)...)
	cmd.Stderr = io.MultiWriter(append(append([]io.Writer{}, stderrWriters...), &captured)...)

	runErr := cmd.Run()
	exitCode := exitCodeOf(runErr)

	if reporting {
		streamer.Close()
		status := classifyStep(exitCode, ansi.Strip(captured.String()), events.Status(*onSuccess))
		if status != "" {
			detail := ""
			if status == events.StatusFailed {
				detail = lastLine(ansi.Strip(captured.String()))
			}
			_ = client.Update(ctx, events.Update{ID: execID, Stack: s, Status: status, Detail: detail})
		}
	}
	return exitCode
}

// capWriter accumulates output, capped so a huge apply does not balloon memory;
// the apply summary is at the tail, so we keep the last ~64 KB.
type capWriter struct{ b []byte }

const capMax = 64 << 10

func (c *capWriter) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)
	if len(c.b) > capMax {
		c.b = c.b[len(c.b)-capMax:]
	}
	return len(p), nil
}
func (c *capWriter) String() string { return string(c.b) }

// exitCodeOf extracts a process exit code from exec.Run's error (0 on success,
// the command's code on ExitError, 1 on any other failure to run).
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// lastLine returns the last non-empty line of s (a thin failure-detail fallback;
// the server backfills the richer error tail from the streamed log).
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
