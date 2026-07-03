package gauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
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

// TestSourceErrorsWithoutCredentials forces both credential paths to fail
// deterministically (bogus ADC file, unreachable metadata host) so the
// no-credentials error path is covered regardless of the host machine.
func TestSourceErrorsWithoutCredentials(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/nonexistent.json")
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
	if _, err := Source(context.Background(), "https://srv.example"); err == nil {
		t.Fatal("Source should error when no credentials are available")
	}
}

// TestSourceTimeout covers the bounded-discovery wrapper on the same
// deterministic no-credentials setup: the underlying error must surface (not
// the timeout) when discovery fails fast.
func TestSourceTimeout(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", t.TempDir()+"/nonexistent.json")
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
	if _, err := SourceTimeout(5*time.Second, "https://srv.example"); err == nil {
		t.Fatal("SourceTimeout should propagate the discovery error")
	} else if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("fast failure must not be reported as a timeout: %v", err)
	}
}
