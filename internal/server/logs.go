package server

import (
	"encoding/json"
	"io"
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
	return store.UpsertStackOutput(a.db, execID, stack, "log", "", excerpt)
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
