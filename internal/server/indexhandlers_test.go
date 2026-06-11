package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

func TestIndexAndPRTimeline(t *testing.T) {
	db := newServerTestDB(t)
	a := New(db, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	_ = store.UpsertInit(db, events.Init{ID: "e1", Repo: "o/r", PR: 7, Environment: "staging"})
	_ = store.UpsertInit(db, events.Init{ID: "e2", Repo: "o/r", PR: 9, Environment: "prod"})

	body := getBody(t, srv.URL+"/")
	for _, want := range []string{"e1", "e2", "/live/e1", "/live/e2", "/assets/app.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}

	pr := getBody(t, srv.URL+"/pr/7")
	if !strings.Contains(pr, "/live/e1") {
		t.Error("pr/7 should list e1")
	}
	if strings.Contains(pr, "/live/e2") {
		t.Error("pr/7 must not list e2 (different PR)")
	}

	resp, _ := http.Get(srv.URL + "/pr/abc")
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("pr/abc status = %d, want 400", resp.StatusCode)
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d, want 200", url, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
