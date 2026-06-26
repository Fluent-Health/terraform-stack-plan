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
	// per-stack plan output drives the stack's detail block on the overview
	_ = store.UpsertStackOutput(db, "e1", "stacks/a", "plan", "", "# report body")

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
	for _, want := range []string{"owner/repo", "staging", "<svg ", "proj-b", `data-plan-url="/plan/e1/stacks/a"`} {
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

	func TestHandleRerun(t *testing.T) {
	db := newServerTestDB(t)
	if err := store.UpsertInit(db, events.Init{
	        ID: "e1", Repo: "owner/repo", SHA: "sha", PR: 7, Environment: "staging",
	        Stacks: []events.StackState{{Path: "stacks/a"}},
	}); err != nil {
	        t.Fatal(err)
	}
	_ = store.SetCheckRunID(db, "e1", 98765)

	var rerunCheckRunID int64
	gh := &MockGitHub{
	        ReRequestCheckRunFn: func(ctx context.Context, repo string, checkRunID int64) error {
	                rerunCheckRunID = checkRunID
	                return nil
	        },
	}
	a := New(db, gh, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/live/e1/rerun", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
	        t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
	        t.Fatalf("POST /live/e1/rerun status = %d, want 204", resp.StatusCode)
	}
	if rerunCheckRunID != 98765 {
	        t.Errorf("rerunCheckRunID = %d; want 98765", rerunCheckRunID)
	}
	}
