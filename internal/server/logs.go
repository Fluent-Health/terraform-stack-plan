package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

// offloadLog uploads a stack's full log buffer to the object store and records
// the key as the stack_outputs pointer (preserving the excerpt). A no-op without
// an object store or a buffer. Best-effort: logged, never fatal.
func (a *App) offloadLog(ctx context.Context, execID, stack string) {
	if a.Objects == nil {
		return
	}
	p, ok := logFilePath(a.cfg.LogsDir, execID, stack)
	if !ok {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return // no buffer → nothing to offload
	}
	defer f.Close()
	key := objectKey(execID, stack)
	if err := a.Objects.Put(ctx, key, f); err != nil {
		log.Printf("offload log %s/%s: %v", execID, stack, err)
		return
	}
	_, excerpt, _, _ := store.GetStackOutput(a.db, execID, stack, "log")
	if err := store.UpsertStackOutput(a.db, execID, stack, "log", key, excerpt); err != nil {
		log.Printf("offload log pointer %s/%s: %v", execID, stack, err)
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

// streamLog upgrades to Server-Sent Events: subscribe first (so nothing is
// missed between replay and live), replay the current buffer, then stream live
// chunks until the client disconnects.
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

	if p, ok := logFilePath(a.cfg.LogsDir, exec, stack); ok {
		if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
			writeSSE(w, string(data))
			flusher.Flush()
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case chunk := <-ch:
			writeSSE(w, chunk)
			flusher.Flush()
		}
	}
}

// writeSSE writes one SSE event: each line of data on its own `data:` field,
// terminated by a blank line.
func writeSSE(w io.Writer, data string) {
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
