package server

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub stands up an httptest server, points apiBase at it for the test,
// and returns it.
func fakeGitHub(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() {
		apiBase = old
		srv.Close()
	})
	return srv
}

func newTestRealClient(t *testing.T) *RealClient {
	t.Helper()
	k := testKey(t)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	c, err := NewRealClient("12345", "67890", pemKey)
	if err != nil {
		t.Fatalf("NewRealClient: %v", err)
	}
	return c
}

func TestNewRealClientValidates(t *testing.T) {
	k := testKey(t)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if _, err := NewRealClient("", "67890", pemKey); err == nil {
		t.Error("want error on empty app id")
	}
	if _, err := NewRealClient("12345", "", pemKey); err == nil {
		t.Error("want error on empty installation id")
	}
	if _, err := NewRealClient("12345", "67890", []byte("bad")); err == nil {
		t.Error("want error on bad key")
	}
}

func TestRealClientImplementsGitHub(t *testing.T) {
	var _ GitHub = (*RealClient)(nil)
}

func TestPRHeadSHAMintsTokenAndReads(t *testing.T) {
	var tokenHits, apiAuth int
	var sawBearerToken string
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/67890/access_tokens":
			tokenHits++
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("token request missing bearer JWT")
			}
			w.Write([]byte(`{"token":"ghs_test"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			apiAuth++
			sawBearerToken = r.Header.Get("Authorization")
			w.Write([]byte(`{"head":{"sha":"deadbeef"}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	})
	c := newTestRealClient(t)
	sha, err := c.PRHeadSHA(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "deadbeef" {
		t.Fatalf("sha = %q, want deadbeef", sha)
	}
	if tokenHits != 1 || apiAuth != 1 {
		t.Fatalf("tokenHits=%d apiAuth=%d, want 1/1", tokenHits, apiAuth)
	}
	if sawBearerToken != "Bearer ghs_test" {
		t.Fatalf("api call used %q, want the minted installation token", sawBearerToken)
	}
}

func TestSplitRepo(t *testing.T) {
	if _, _, err := splitRepo("noslash"); err == nil {
		t.Error("want error")
	}
	o, n, err := splitRepo("owner/name")
	if err != nil || o != "owner" || n != "name" {
		t.Fatalf("splitRepo = %q/%q/%v", o, n, err)
	}
}
