package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusContext(t *testing.T) {
	if got := statusContext("staging"); got != "plan/staging" {
		t.Errorf("statusContext(staging) = %q", got)
	}
	if got := statusContext(""); got != "plan" {
		t.Errorf("statusContext(\"\") = %q", got)
	}
}

func TestHealthz(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}
}

func TestBearerAuth(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{WebhookSecret: "s3cret"})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	for _, h := range []string{"", "Bearer wrong", "s3cret"} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q = %d, want 401", h, resp.StatusCode)
		}
	}
}

func TestAuthDisabledWhenSecretEmpty(t *testing.T) {
	a := New(nil, &MockGitHub{}, Config{})
	srv := httptest.NewServer(a.Routes())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/init", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("empty secret must disable auth, got 401")
	}
}
