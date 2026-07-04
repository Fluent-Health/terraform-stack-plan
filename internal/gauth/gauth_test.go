package gauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

// staticTokenSource returns a fixed token (or error) without any network.
type staticTokenSource struct {
	t   *oauth2.Token
	err error
}

func (s staticTokenSource) Token() (*oauth2.Token, error) { return s.t, s.err }

func TestFromTokenSourcePicksIDToken(t *testing.T) {
	tok := (&oauth2.Token{AccessToken: "at"}).WithExtra(map[string]any{"id_token": "idt-123"})
	fn := fromTokenSource(staticTokenSource{t: tok}, func(t *oauth2.Token) string {
		id, _ := t.Extra("id_token").(string)
		return id
	})
	got, err := fn(context.Background())
	if err != nil || got != "idt-123" {
		t.Fatalf("token = %q, %v; want idt-123", got, err)
	}
}

func TestFromTokenSourceMissingIDToken(t *testing.T) {
	fn := fromTokenSource(staticTokenSource{t: &oauth2.Token{AccessToken: "at"}}, func(t *oauth2.Token) string {
		id, _ := t.Extra("id_token").(string)
		return id
	})
	if _, err := fn(context.Background()); err == nil || !strings.Contains(err.Error(), "no ID token") {
		t.Fatalf("err = %v, want a no-ID-token explanation", err)
	}
}

func TestFromTokenSourcePropagatesError(t *testing.T) {
	boom := errors.New("refresh failed")
	fn := fromTokenSource(staticTokenSource{err: boom}, func(*oauth2.Token) string { return "" })
	if _, err := fn(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// blockingTokenSource never returns — the context bound must kick in.
type blockingTokenSource struct{}

func (blockingTokenSource) Token() (*oauth2.Token, error) {
	select {} // block forever
}

func TestTokenWithContextHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := tokenWithContext(ctx, blockingTokenSource{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("token fetch was not bounded by the context")
	}
}

// fakeNonGCE points the metadata probe at a server that answers without the
// Metadata-Flavor header — a fast, definitive "not on GCE" (an unreachable
// host would make the probe retry with backoff and slow the test down).
func fakeNonGCE(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "not gce")
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
}

// TestSourceErrorsWithoutCredentials forces every credential path to fail
// deterministically (bogus ADC file, non-GCE metadata) so the no-credentials
// error path is covered regardless of the host machine.
func TestSourceErrorsWithoutCredentials(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/nonexistent.json")
	fakeNonGCE(t)
	if _, err := Source(context.Background(), "https://srv.example"); err == nil {
		t.Fatal("Source should error when no credentials are available")
	}
}

// iamCredsFake serves generateIdToken regardless of URL (the impersonate
// package pins the real endpoint; the injected client's transport hijacks it).
type iamCredsFake struct {
	t         *testing.T
	wantEmail string
	token     string
	gotAud    string
}

func (f *iamCredsFake) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Path, f.wantEmail+":generateIdToken") {
		f.t.Errorf("unexpected iamcredentials path %s", req.URL.Path)
	}
	var body struct {
		Audience string `json:"audience"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	f.gotAud = body.Audience
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"token":"` + f.token + `"}`)),
		Request:    req,
	}, nil
}

// TestSelfIDTokenSource: the Cloud Build fallback mints via the IAM
// Credentials generateIdToken API, targeting the ambient SA itself.
func TestSelfIDTokenSource(t *testing.T) {
	fake := &iamCredsFake{t: t, wantEmail: "tf-planner@x.iam.gserviceaccount.com", token: "cb-id-token"}
	fn, err := selfIDTokenSource(context.Background(), "https://serve.example", "tf-planner@x.iam.gserviceaccount.com",
		option.WithHTTPClient(&http.Client{Transport: fake}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := fn(context.Background())
	if err != nil || got != "cb-id-token" {
		t.Fatalf("token = %q, %v; want cb-id-token", got, err)
	}
	if fake.gotAud != "https://serve.example" {
		t.Errorf("audience sent = %q", fake.gotAud)
	}
}

// TestSourceCloudBuildFallback fakes Cloud Build's metadata surface: the
// server answers the ADC probes and the email endpoint but 404s the identity
// endpoint (the documented Cloud Build gap). Source must route to the IAM
// Credentials self-impersonation path instead of failing with "credentials
// carry no ID token".
func TestSourceCloudBuildFallback(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Metadata-Flavor", "Google")
		switch {
		case strings.HasSuffix(r.URL.Path, "/identity") || strings.Contains(r.URL.RawQuery, "audience"):
			http.Error(w, "identity endpoint not implemented", http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/email"):
			io.WriteString(w, "tf-planner@x.iam.gserviceaccount.com")
		case strings.HasSuffix(r.URL.Path, "/token"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
		default:
			io.WriteString(w, "ok")
		}
	}))
	defer meta.Close()
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(meta.URL, "http://"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	fn, err := Source(context.Background(), "https://serve.example")
	if err != nil {
		t.Fatalf("Source should fall back to IAM Credentials self-impersonation, got %v", err)
	}
	if fn == nil {
		t.Fatal("nil TokenFunc")
	}
	// Not invoked: calling would hit the real iamcredentials endpoint. The
	// mint itself is covered by TestSelfIDTokenSource with a fake endpoint.
}

// TestSourceTimeout covers the bounded-discovery wrapper on the same
// deterministic no-credentials setup: the underlying error must surface (not
// the timeout) when discovery fails fast.
func TestSourceTimeout(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/nonexistent.json")
	fakeNonGCE(t)
	if _, err := SourceTimeout(5*time.Second, "https://srv.example"); err == nil {
		t.Fatal("SourceTimeout should propagate the discovery error")
	} else if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("fast failure must not be reported as a timeout: %v", err)
	}
}
