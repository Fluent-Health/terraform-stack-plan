package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
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

func TestLogsE2E(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir(), WebhookSecret: "s"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	post := func(c events.LogChunk) int {
		b, _ := json.Marshal(c)
		req, _ := http.NewRequest("POST", srv.URL+"/api/logs", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer s")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(events.LogChunk{ID: "e1", Stack: "stacks/a", Data: "one\n"}); c != 200 {
		t.Fatalf("post1 = %d", c)
	}
	if c := post(events.LogChunk{ID: "e1", Stack: "stacks/a", Data: "two\n"}); c != 200 {
		t.Fatalf("post2 = %d", c)
	}

	resp, err := http.Get(srv.URL + "/logs/e1/stacks/a")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "one\ntwo\n" {
		t.Fatalf("logs read = %d %q", resp.StatusCode, body)
	}

	r2, _ := http.NewRequest("POST", srv.URL+"/api/logs", bytes.NewReader([]byte(`{}`)))
	resp2, _ := http.DefaultClient.Do(r2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/logs without bearer = %d, want 401", resp2.StatusCode)
	}

	r3, _ := http.Get(srv.URL + "/logs/e1/stacks/nope")
	r3.Body.Close()
	if r3.StatusCode != 404 {
		t.Errorf("unknown log = %d, want 404", r3.StatusCode)
	}
}
