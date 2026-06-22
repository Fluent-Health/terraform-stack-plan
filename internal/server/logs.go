package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// excerptBytes is the tail size mirrored in the DB for instant render.
const excerptBytes = 16 << 10

// sanitizeComponent reduces an untrusted path component to a safe flat token:
// path separators and ".." become "_", so it can't traverse.
func sanitizeComponent(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	return s
}

// logFilePath builds the buffer path for (exec, stack) under base, and reports
// whether it is safely contained within base (defense in depth).
func logFilePath(base, exec, stack string) (string, bool) {
	if base == "" {
		return "", false
	}
	p := filepath.Join(base, sanitizeComponent(exec), sanitizeComponent(stack)+".log")
	cleanBase := filepath.Clean(base) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(p), cleanBase) {
		return "", false
	}
	return p, true
}

// appendLog appends a chunk to the stack's buffer and mirrors the tail excerpt
// into the DB. A no-op when LogsDir is unset.
func (a *App) appendLog(execID, stack, data string) error {
	p, ok := logFilePath(a.cfg.LogsDir, execID, stack)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	excerpt, err := tailFile(p, excerptBytes)
	if err != nil {
		return err
	}
	if err := store.UpsertStackOutput(a.db, execID, stack, "log", "", excerpt); err != nil {
		return err
	}
	if a.hub != nil {
		a.hub.publish(execID+"|"+stack, data)
	}
	return nil
}

// errNoObjectStore reports that offload is impossible (no store configured).
// Only its non-nil-ness is used today: finalizeLogs must treat "no store" as
// not-offloaded so it never deletes the only copy of a buffer.
var errNoObjectStore = errors.New("no object store")

// offloadLog uploads a stack's full log buffer to the object store and records
// the key as the stack_outputs pointer (preserving the excerpt). Returns
// errNoObjectStore without a store, nil when there was no buffer to offload.
func (a *App) offloadLog(ctx context.Context, execID, stack string) error {
	if a.Objects == nil {
		return errNoObjectStore
	}
	p, ok := logFilePath(a.cfg.LogsDir, execID, stack)
	if !ok {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil // no buffer → nothing to offload
	}
	defer f.Close()
	key := objectKey(execID, stack)
	if err := a.Objects.Put(ctx, key, f); err != nil {
		log.Printf("offload log %s/%s: %v", execID, stack, err)
		return err
	}
	_, excerpt, _, _ := store.GetStackOutput(a.db, execID, stack, "log")
	if err := store.UpsertStackOutput(a.db, execID, stack, "log", key, excerpt); err != nil {
		log.Printf("offload log pointer %s/%s: %v", execID, stack, err)
		return err
	}
	return nil
}

// finalizeLogs offloads every stack's buffer and, when ALL offloads succeeded,
// deletes the execution's buffer directory (the object store is now the source
// of truth; the buffer volume is small). Best-effort: failures keep the buffer.
func (a *App) finalizeLogs(ctx context.Context, execID string, stacks []events.StackState) {
	ok := true
	for _, s := range stacks {
		if err := a.offloadLog(ctx, execID, s.Path); err != nil {
			ok = false
		}
	}
	if !ok || a.cfg.LogsDir == "" {
		return
	}
	dir := filepath.Join(a.cfg.LogsDir, sanitizeComponent(execID))
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("clean log buffers %s: %v", execID, err)
	}
}

// CleanLogBuffers removes execution buffer directories older than maxAge —
// orphans left by restarts mid-execution. Call at startup.
func (a *App) CleanLogBuffers(maxAge time.Duration) {
	if a.cfg.LogsDir == "" {
		return
	}
	entries, err := os.ReadDir(a.cfg.LogsDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(a.cfg.LogsDir, e.Name())); err != nil {
			log.Printf("clean log buffers: %s: %v", e.Name(), err)
		}
	}
}

// tailFile returns the last n bytes of a file as a string.
func tailFile(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if info.Size() > n {
		start = info.Size() - n
	}
	if _, err := f.Seek(start, 0); err != nil {
		return "", err
	}
	b := make([]byte, info.Size()-start)
	if _, err := io.ReadFull(f, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// handleLogServe streams a stack's log buffer (public, like the live page —
// viewers need no cloud IAM). 404 when there is no buffer.
// With ?follow=1 it upgrades to Server-Sent Events.
func (a *App) handleLogServe(w http.ResponseWriter, r *http.Request) {
	exec := r.PathValue("exec")
	stack := r.PathValue("stack")
	if r.URL.Query().Get("follow") != "" {
		a.streamLog(w, r, exec, stack)
		return
	}
	// static serve: prefer the live buffer, else the offloaded object.
	if p, ok := logFilePath(a.cfg.LogsDir, exec, stack); ok {
		if f, err := os.Open(p); err == nil {
			defer f.Close()
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.Copy(w, f)
			return
		}
	}
	if a.Objects != nil {
		if pointer, _, ok, _ := store.GetStackOutput(a.db, exec, stack, "log"); ok && pointer != "" {
			if rc, err := a.Objects.Get(r.Context(), pointer); err == nil {
				defer rc.Close()
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = io.Copy(w, rc)
				return
			}
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// handlePlanServe renders one stack's stored plan section to HTML on demand
// (public, like handleLogServe). The live page fetches this when a stack's
// Result pane is opened, so the page load no longer renders every stack's plan.
// 200 with an empty body when no plan is stored — the viewer shows its own
// "no plan" state. The plan markdown is trusted (render-core output).
func (a *App) handlePlanServe(w http.ResponseWriter, r *http.Request) {
	exec := r.PathValue("exec")
	stack := r.PathValue("stack")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, md, ok, _ := store.GetStackOutput(a.db, exec, stack, "plan"); ok && md != "" {
		_, _ = io.WriteString(w, string(renderMarkdown(md)))
	}
}

// streamLog upgrades to Server-Sent Events: subscribe first (so nothing is
// missed between replay and live), replay the current buffer (streamed, from the
// Last-Event-ID byte offset if resuming), then stream live chunks until the
// client disconnects. A periodic comment heartbeat keeps the connection alive.
func (a *App) streamLog(w http.ResponseWriter, r *http.Request, exec, stack string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch, unsub := a.hub.subscribe(exec + "|" + stack)
	defer unsub()

	var offset int64
	if id := strings.TrimSpace(r.Header.Get("Last-Event-ID")); id != "" {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && n >= 0 {
			offset = n
		}
	}
	servedBuffer := false
	if p, ok := logFilePath(a.cfg.LogsDir, exec, stack); ok {
		if f, err := os.Open(p); err == nil {
			servedBuffer = true
			if _, err := f.Seek(offset, io.SeekStart); err == nil {
				buf := make([]byte, 32<<10)
				for {
					if r.Context().Err() != nil {
						f.Close()
						return
					}
					n, e := f.Read(buf)
					if n > 0 {
						offset += int64(n)
						writeSSEEvent(w, offset, string(buf[:n]))
						flusher.Flush()
					}
					if e != nil {
						break
					}
				}
			}
			f.Close()
		}
	}
	// No live buffer: either the run hasn't written yet, or it concluded and the
	// buffer was offloaded + deleted at finalize. A live page left open across
	// finalize re-opens follow connections as the user switches stacks; those find
	// no buffer, so without this they would replay nothing and hang forever. If an
	// offloaded object exists the run is over — replay it and end the stream.
	if !servedBuffer && a.streamOffloaded(r.Context(), w, flusher, exec, stack, &offset) {
		writeSSEDone(w)
		flusher.Flush()
		return
	}
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case chunk := <-ch:
			offset += int64(len(chunk))
			writeSSEEvent(w, offset, chunk)
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// streamOffloaded replays the offloaded (object-store) log over SSE for a
// concluded run whose live buffer is gone, resuming at *offset bytes. It reports
// whether an offloaded object existed: false means there is nothing archived yet
// (so the caller keeps waiting for live chunks). Best-effort — a mid-stream read
// error just ends the replay with whatever was already sent.
func (a *App) streamOffloaded(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, exec, stack string, offset *int64) bool {
	if a.Objects == nil {
		return false
	}
	pointer, _, ok, _ := store.GetStackOutput(a.db, exec, stack, "log")
	if !ok || pointer == "" {
		return false
	}
	rc, err := a.Objects.Get(ctx, pointer)
	if err != nil {
		return false
	}
	defer rc.Close()
	if *offset > 0 {
		// Skip what the buffer replay (or a Last-Event-ID resume) already sent.
		if _, err := io.CopyN(io.Discard, rc, *offset); err != nil {
			return true
		}
	}
	buf := make([]byte, 32<<10)
	for {
		if ctx.Err() != nil {
			return true
		}
		n, e := rc.Read(buf)
		if n > 0 {
			*offset += int64(n)
			writeSSEEvent(w, *offset, string(buf[:n]))
			flusher.Flush()
		}
		if e != nil {
			break
		}
	}
	return true
}

// writeSSEDone emits a terminal "done" event so the client closes the stream; a
// concluded run sends no more chunks, and without this the EventSource would
// auto-retry the (now buffer-less) follow connection in a loop.
func writeSSEDone(w io.Writer) {
	fmt.Fprint(w, "event: done\ndata: \n\n")
}

// writeSSE writes one SSE event: each line of data on its own `data:` field,
// terminated by a blank line.
func writeSSE(w io.Writer, data string) {
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

// writeSSEEvent writes an id-tagged SSE event (id = cumulative byte offset, for
// Last-Event-ID resume) followed by the data lines.
func writeSSEEvent(w io.Writer, id int64, data string) {
	fmt.Fprintf(w, "id: %d\n", id)
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

// handleLogs ingests a per-stack output chunk (bearer-authed).
func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	var c events.LogChunk
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		badRequest(w, err)
		return
	}
	if err := a.appendLog(c.ID, c.Stack, c.Data); err != nil {
		http.Error(w, "append log", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
