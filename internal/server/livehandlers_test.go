package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b := new(strings.Builder)
	if _, err := io.Copy(b, resp.Body); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestImgAndLiveHandlers(t *testing.T) {
	db := newServerTestDB(t)
	if err := store.UpsertInit(db, events.Init{
		ID: "e1", Repo: "owner/repo", SHA: "sha", PR: 7, Environment: "staging",
		Stacks: []events.StackState{
			{Path: "stacks/a", Status: events.StatusPlanned},
			{Path: "stacks/b", Status: events.StatusGated, Project: "proj-b"},
		},
		Edges: []events.Edge{{From: "stacks/a", To: "stacks/b"}},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.SetReport(db, "e1", "# report body")
	_ = store.UpsertTarget(db, 7, "staging", "iam", "proj-b", "", "AWAITING")

	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/img/e1.svg")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("/img status=%d ct=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.HasPrefix(body, "<svg ") {
		t.Errorf("/img body not svg: %.40s", body)
	}

	resp2, err := http.Get(srv.URL + "/live/e1")
	if err != nil {
		t.Fatal(err)
	}
	page := readBody(t, resp2)
	if resp2.StatusCode != 200 || !strings.HasPrefix(resp2.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("/live status=%d ct=%q", resp2.StatusCode, resp2.Header.Get("Content-Type"))
	}
	for _, want := range []string{"owner/repo", "staging", "<svg ", "proj-b", "# report body"} {
		if !strings.Contains(page, want) {
			t.Errorf("/live page missing %q", want)
		}
	}

	for _, p := range []string{"/img/nope.svg", "/live/nope"} {
		r, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Errorf("%s status=%d, want 404", p, r.StatusCode)
		}
	}

	aSecret := New(db, &MockGitHub{}, Config{WebhookSecret: "s"})
	srv2 := httptest.NewServer(aSecret.Routes())
	defer srv2.Close()
	r, err := http.Get(srv2.URL + "/img/e1.svg")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode == http.StatusUnauthorized {
		t.Error("/img must be public (no auth)")
	}
}

func TestStackDetailPage(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Environment: "staging",
		Stacks: []events.StackState{{Path: "stacks/a"}}})
	_ = store.UpsertStackOutput(db, "e1", "stacks/a", "plan", "", "PLAN_SECTION_A")
	_ = store.UpsertStackOutput(db, "e1", "stacks/a", "log", "", "LOG_TAIL_A")

	resp, err := http.Get(srv.URL + "/live/e1/stack/stacks/a")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	s := string(body)
	for _, want := range []string{
		"stacks/a", "PLAN_SECTION_A", "LOG_TAIL_A",
		`/logs/e1/stacks/a`, "tabs", "Verify", "/assets/app.css",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("stack page missing %q", want)
		}
	}

	resp2, _ := http.Get(srv.URL + "/live/nope/stack/stacks/a")
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Errorf("unknown exec status = %d, want 404", resp2.StatusCode)
	}
}

func TestStackDetailVerifyLink(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "plan-1", Repo: "o/r", PR: 7, Environment: "staging",
		Context: "plan/staging", Stacks: []events.StackState{{Path: "stacks/a"}}})
	_ = store.UpsertInit(db, events.Init{ID: "verify-9", Repo: "o/r", PR: 7, Environment: "staging",
		Context: "verify/staging"})

	resp, _ := http.Get(srv.URL + "/live/plan-1/stack/stacks/a")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "/live/verify-9") {
		t.Errorf("Verify tab should link to the latest verify run /live/verify-9")
	}
}

func TestStackDetailLogFollow(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Stacks: []events.StackState{{Path: "stacks/a"}}})

	resp, _ := http.Get(srv.URL + "/live/e1/stack/stacks/a")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := string(body)
	for _, want := range []string{`id="stacklog"`, "new EventSource", `/logs/e1/stacks/a?follow=1`, "<noscript>"} {
		if !strings.Contains(s, want) {
			t.Errorf("stack page Log tab missing %q", want)
		}
	}
}

func TestDrivePublishesChange(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Stacks: []events.StackState{{Path: "s/a"}}})

	ch, unsub := a.hub.subscribe("exec:e1")
	defer unsub()
	a.drive(context.Background(), "e1", "", false)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("drive did not publish a change to exec:e1")
	}
}

func TestLiveEventsSSE(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", Stacks: []events.StackState{{Path: "s/a"}}})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp404, _ := http.Get(srv.URL + "/live/nope/events")
	resp404.Body.Close()
	if resp404.StatusCode != 404 {
		t.Errorf("unknown exec events = %d, want 404", resp404.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/live/e1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	go func() { time.Sleep(100 * time.Millisecond); a.drive(context.Background(), "e1", "", false) }()
	sc := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !sc.Scan() {
			break
		}
		if strings.Contains(sc.Text(), "changed") {
			return
		}
	}
	t.Fatal("did not receive a 'changed' SSE event")
}
