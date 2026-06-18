package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedCSSNonEmpty(t *testing.T) {
	if len(appCSS) == 0 {
		t.Fatal("embedded app.css is empty — run web/build.sh")
	}
}

// TestBriefingComponentsInCSS guards the built stylesheet: the one bespoke piece
// (the segmented progress bar + dynamic state-colour classes, plain CSS in
// web/input.css so they survive tree-shaking) AND the DaisyUI components the
// template relies on (emitted because the template references them literally).
// If this fails, rerun web/build.sh after editing web/input.css or the template.
func TestBriefingComponentsInCSS(t *testing.T) {
	css := string(appCSS)
	for _, cls := range []string{
		".progress-bar", ".bar-seg", ".bs-applying", ".sl-applying", // bespoke progress + state colours
		".stack-detail", ".term", ".a-green", ".live-dot", ".shimmer", // :target swap + softened log + live/loading cues
		".badge", ".menu", ".card", ".collapse", ".tabs", // DaisyUI components reused by the template
	} {
		if !strings.Contains(css, cls) {
			t.Errorf("app.css missing %q — run web/build.sh after editing web/input.css or the template", cls)
		}
	}
}

func TestReportCSSServed(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/report.css", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("report.css: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), ".tfsp-diff") {
		t.Fatalf("report.css missing diff styles")
	}
}

func TestServeAsset(t *testing.T) {
	a := New(newServerTestDB(t), &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content-type = %q, want text/css", ct)
	}

	resp2, err := http.Get(srv.URL + "/assets/nope.css")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 404 {
		t.Errorf("unknown asset status = %d, want 404", resp2.StatusCode)
	}
}
