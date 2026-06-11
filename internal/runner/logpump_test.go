package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// fakePoster records posted chunks.
type fakePoster struct {
	mu     sync.Mutex
	chunks []events.LogChunk
}

func (f *fakePoster) LogChunk(_ context.Context, c events.LogChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, c)
	return nil
}

func (f *fakePoster) concat(stack string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s string
	for _, c := range f.chunks {
		if c.Stack == stack {
			s += c.Data
		}
	}
	return s
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

func TestLogPumpDeltaOffset(t *testing.T) {
	root := t.TempDir()
	fp := &fakePoster{}
	lp := NewLogPump(fp, root, "tfstackplan.log", "e1")

	path := filepath.Join(root, "stacks/a", "tfstackplan.log")
	writeFile(t, path, "line 1\n")
	lp.pump(context.Background(), "stacks/a")
	appendFile(t, path, "line 2\n")
	lp.pump(context.Background(), "stacks/a")

	if got := fp.concat("stacks/a"); got != "line 1\nline 2\n" {
		t.Fatalf("concat = %q, want the full file once", got)
	}
	lp.pump(context.Background(), "stacks/missing")
	if got := fp.concat("stacks/missing"); got != "" {
		t.Errorf("missing-file pump posted %q", got)
	}
}

func TestLogPumpStartStopFlush(t *testing.T) {
	root := t.TempDir()
	fp := &fakePoster{}
	lp := NewLogPump(fp, root, "tfstackplan.log", "e1")
	lp.interval = 10 * time.Millisecond

	pa := filepath.Join(root, "stacks/a", "tfstackplan.log")
	pb := filepath.Join(root, "stacks/b", "tfstackplan.log")
	writeFile(t, pa, "a-1\n")
	writeFile(t, pb, "b-1\n")

	stop := lp.Start([]string{"stacks/a", "stacks/b"})
	time.Sleep(40 * time.Millisecond)
	appendFile(t, pa, "a-2\n") // appended just before stop; final flush must catch it
	stop()

	if got := fp.concat("stacks/a"); got != "a-1\na-2\n" {
		t.Errorf("stacks/a = %q, want a-1\\na-2\\n", got)
	}
	if got := fp.concat("stacks/b"); got != "b-1\n" {
		t.Errorf("stacks/b = %q, want b-1\\n", got)
	}
}
