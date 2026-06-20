package main

import (
	"regexp"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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
