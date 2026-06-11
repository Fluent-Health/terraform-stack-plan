package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// logPoster posts a log chunk to the server. *Client satisfies it.
type logPoster interface {
	LogChunk(ctx context.Context, c events.LogChunk) error
}

// LogPump tails each stack's on-disk log file and posts newly-appended bytes to
// the server. It is the parallel-safe path to per-stack logs: terramate's
// combined `script run` stream interleaves stacks with no per-stack attribution,
// so by convention each stack's script writes its own file
// (`<root>/<stack>/<file>`, e.g. via `terraform plan ... 2>&1 | tee tfstackplan.log`)
// and the pump tails it. All posts are best-effort.
type LogPump struct {
	poster   logPoster
	root     string
	file     string
	execID   string
	interval time.Duration
	offsets  map[string]int64 // bytes already posted, per stack
}

// NewLogPump builds a pump that posts via p, reading the file named `file` in
// each stack dir under root, tagging chunks with execID.
func NewLogPump(p logPoster, root, file, execID string) *LogPump {
	return &LogPump{
		poster:   p,
		root:     root,
		file:     file,
		execID:   execID,
		interval: 2 * time.Second,
		offsets:  map[string]int64{},
	}
}

// pump posts any bytes appended to a stack's log file since the last successful
// post. A missing file (stack not started) is a no-op. On a post failure the
// offset is not advanced, so the bytes are retried on the next tick.
func (lp *LogPump) pump(ctx context.Context, stack string) {
	path := filepath.Join(lp.root, filepath.FromSlash(stack), lp.file)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	off := lp.offsets[stack]
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return
	}
	if err := lp.poster.LogChunk(ctx, events.LogChunk{ID: lp.execID, Stack: stack, Data: string(data)}); err != nil {
		return
	}
	lp.offsets[stack] = off + int64(len(data))
}

func (lp *LogPump) pumpAll(ctx context.Context, stacks []string) {
	for _, s := range stacks {
		lp.pump(ctx, s)
	}
}

// Start begins tailing in the background and returns a stop function that does a
// final flush and waits for the goroutine to exit. The pump runs in a single
// goroutine, so offsets need no locking.
func (lp *LogPump) Start(stacks []string) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(lp.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				fctx, fcancel := context.WithTimeout(context.Background(), 10*time.Second)
				lp.pumpAll(fctx, stacks) // final flush
				fcancel()
				return
			case <-t.C:
				lp.pumpAll(ctx, stacks)
			}
		}
	}()
	return func() { cancel(); <-done }
}
