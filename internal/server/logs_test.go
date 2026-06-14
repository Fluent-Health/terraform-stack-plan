package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
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
		{"..", ".."},
		{"e1", "....//....//etc"},
	} {
		p, ok := logFilePath(base, bad.exec, bad.stack)
		if !ok {
			continue // rejected outright — also safe
		}
		if !strings.HasPrefix(filepath.Clean(p)+string(filepath.Separator), filepath.Clean(base)+string(filepath.Separator)) {
			t.Errorf("traversal escaped base: exec=%q stack=%q → %q (base %q)", bad.exec, bad.stack, p, base)
		}
		// Belt-and-suspenders: the resolved path must not contain a parent ref.
		if strings.Contains(p, ".."+string(filepath.Separator)) {
			t.Errorf("sanitized path still has a parent ref: exec=%q stack=%q → %q", bad.exec, bad.stack, p)
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

	apiTok, _ := jwtutil.Make("s", "runner", "api", time.Hour)
	post := func(c events.LogChunk) int {
		b, _ := json.Marshal(c)
		req, _ := http.NewRequest("POST", srv.URL+"/api/logs", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+apiTok)
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

func TestLogStreamSSE(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	if err := a.appendLog("e1", "stacks/a", "before\n"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/logs/e1/stacks/a?follow=1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	var mu sync.Mutex
	var buf strings.Builder
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			mu.Lock()
			buf.WriteString(sc.Text())
			buf.WriteByte('\n')
			mu.Unlock()
		}
	}()
	seen := func(sub string) bool { mu.Lock(); defer mu.Unlock(); return strings.Contains(buf.String(), sub) }
	waitFor := func(sub string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for !seen(sub) {
			select {
			case <-deadline:
				mu.Lock()
				got := buf.String()
				mu.Unlock()
				t.Fatalf("timed out waiting for %q; got:\n%s", sub, got)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	waitFor("data: before")
	if err := a.appendLog("e1", "stacks/a", "after\n"); err != nil {
		t.Fatal(err)
	}
	waitFor("data: after")
}

func TestLogStreamResumeFromLastEventID(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	if err := a.appendLog("e1", "s/a", "line1\nline2\nline3\n"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/logs/e1/s/a?follow=1", nil)
	req.Header.Set("Last-Event-ID", "6") // skip "line1\n"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	var got strings.Builder
	sawID := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !sc.Scan() {
			break
		}
		ln := sc.Text()
		if strings.HasPrefix(ln, "id: ") {
			sawID = true
		}
		if strings.HasPrefix(ln, "data: ") {
			got.WriteString(strings.TrimPrefix(ln, "data: "))
		}
		if strings.Contains(got.String(), "line3") {
			break
		}
	}
	out := got.String()
	if !sawID {
		t.Error("expected id: lines in the stream")
	}
	if strings.Contains(out, "line1") {
		t.Errorf("resume from offset 6 should skip line1; got %q", out)
	}
	if !strings.Contains(out, "line2") || !strings.Contains(out, "line3") {
		t.Errorf("resume should include line2+line3; got %q", out)
	}
}

func TestOffloadAndServeFromStore(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	a.Objects = FSStore{Root: t.TempDir()}

	if err := a.appendLog("e1", "stacks/a", "archived log\n"); err != nil {
		t.Fatal(err)
	}
	_ = a.offloadLog(context.Background(), "e1", "stacks/a")

	pointer, _, ok, _ := store.GetStackOutput(db, "e1", "stacks/a", "log")
	if !ok || pointer == "" {
		t.Fatalf("pointer not set: %q ok=%v", pointer, ok)
	}

	// Simulate buffer cleanup; the public read must fall back to the store.
	bufp, _ := logFilePath(a.cfg.LogsDir, "e1", "stacks/a")
	os.Remove(bufp)

	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/logs/e1/stacks/a")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "archived log\n" {
		t.Fatalf("served = %d %q (want from object store)", resp.StatusCode, body)
	}
}

func TestHandleUpdateTriggersOffloadOnTerminal(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	a.Objects = FSStore{Root: t.TempDir()}
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}}})
	if err := a.appendLog("e1", "stacks/a", "done log\n"); err != nil {
		t.Fatal(err)
	}
	post(t, srv, "/api/update", events.Update{ID: "e1", Stack: "stacks/a", Status: events.StatusPlanned})

	pointer, _, ok, _ := store.GetStackOutput(db, "e1", "stacks/a", "log")
	if !ok || pointer == "" {
		t.Errorf("terminal update should offload the log (pointer=%q ok=%v)", pointer, ok)
	}
}

// memObjects is an in-memory ObjectStore fake.
type memObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemObjects() *memObjects { return &memObjects{data: map[string][]byte{}} }

func (m *memObjects) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	return nil
}

func (m *memObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func TestFinalizeLogsOffloadsAndDeletesBuffers(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	mem := newMemObjects()
	a.Objects = mem

	if err := store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", SHA: "s",
		Stacks: []events.StackState{{Path: "stacks/a", Status: events.StatusPlanned}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.appendLog("e1", "stacks/a", "hello\n"); err != nil {
		t.Fatal(err)
	}

	a.finalizeLogs(context.Background(), "e1", []events.StackState{{Path: "stacks/a"}})

	if _, ok := mem.data[objectKey("e1", "stacks/a")]; !ok {
		t.Fatalf("log not offloaded to object store")
	}
	execDir := filepath.Join(a.cfg.LogsDir, sanitizeComponent("e1"))
	if _, err := os.Stat(execDir); !os.IsNotExist(err) {
		t.Fatalf("buffer dir survived finalize: %v", err)
	}
	// The static log route must now fall through to the object store.
	req := httptest.NewRequest(http.MethodGet, "/logs/e1/stacks/a", nil)
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("static log after cleanup: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestFinalizeLogsKeepsBuffersWhenOffloadFails(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{LogsDir: t.TempDir()})
	// nil Objects → offload is a no-op → buffers must survive.
	if err := a.appendLog("e1", "stacks/a", "hello\n"); err != nil {
		t.Fatal(err)
	}
	a.finalizeLogs(context.Background(), "e1", []events.StackState{{Path: "stacks/a"}})
	p, _ := logFilePath(a.cfg.LogsDir, "e1", "stacks/a")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("buffer deleted without offload: %v", err)
	}
}

func TestCleanLogBuffersSweepsOldDirs(t *testing.T) {
	dir := t.TempDir()
	a := New(newServerTestDB(t), &MockGitHub{}, Config{LogsDir: dir})
	old := filepath.Join(dir, "old-exec")
	fresh := filepath.Join(dir, "fresh-exec")
	for _, d := range []string{old, fresh} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	a.CleanLogBuffers(24 * time.Hour)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stale dir survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh dir swept: %v", err)
	}
}
