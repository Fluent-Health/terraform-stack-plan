package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestLogFilePathSanitizesAndContains(t *testing.T) {
	base := t.TempDir()
	p, ok := logFilePath(base, "e1", "stacks/a")
	if !ok || !strings.HasPrefix(p, base) {
		t.Fatalf("path = %q ok=%v", p, ok)
	}
	for _, bad := range []struct{ exec, stack string }{
		{"../../etc", "x"},
		{"e1", "../../../etc/passwd"},
		{"e1", "a/../../b"},
	} {
		p, ok := logFilePath(base, bad.exec, bad.stack)
		if ok && !strings.HasPrefix(filepath.Clean(p), filepath.Clean(base)) {
			t.Errorf("traversal escaped base: exec=%q stack=%q → %q", bad.exec, bad.stack, p)
		}
	}
}

func TestAppendLogWritesBufferAndExcerpt(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	if err := a.appendLog("e1", "stacks/a", "line1\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.appendLog("e1", "stacks/a", "line2\n"); err != nil {
		t.Fatal(err)
	}
	p, _ := logFilePath(a.cfg.LogsDir, "e1", "stacks/a")
	data, _ := os.ReadFile(p)
	if string(data) != "line1\nline2\n" {
		t.Fatalf("buffer = %q", data)
	}
	_, excerpt, ok, _ := store.GetStackOutput(db, "e1", "stacks/a", "log")
	if !ok || !strings.Contains(excerpt, "line2") {
		t.Fatalf("excerpt = %q ok=%v", excerpt, ok)
	}
}

func TestAppendLogDisabledWithoutLogsDir(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	if err := a.appendLog("e1", "stacks/a", "x"); err != nil {
		t.Errorf("appendLog without LogsDir should no-op, got %v", err)
	}
}
