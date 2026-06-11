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
