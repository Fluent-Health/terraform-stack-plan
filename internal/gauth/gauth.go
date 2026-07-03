// Package gauth obtains Google-signed OIDC ID tokens for authenticating to
// the tfstackplan control plane, replacing the shared-secret HS256 scheme.
// Two Application Default Credential shapes are supported:
//
//   - service accounts (Cloud Build / GCE metadata server, SA keys,
//     impersonation): idtoken.NewTokenSource mints tokens for the requested
//     audience — the serve URL;
//   - user credentials (`gcloud auth application-default login`), which cannot
//     mint custom-audience tokens: the id_token riding the OAuth refresh
//     response is used instead. Its audience is the gcloud ADC client id, so
//     the server must list it in api_auth.extra_audiences.
package gauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
)

// TokenFunc returns a currently-valid bearer token. Implementations cache and
// refresh internally; callers invoke it per request.
type TokenFunc func(ctx context.Context) (string, error)

// Source builds a TokenFunc yielding Google OIDC ID tokens for audience from
// Application Default Credentials. Errors when no ADC is available at all.
func Source(ctx context.Context, audience string) (TokenFunc, error) {
	if ts, err := idtoken.NewTokenSource(ctx, audience); err == nil {
		return fromTokenSource(ts, func(t *oauth2.Token) string { return t.AccessToken }), nil
	}
	// idtoken refuses user ADC ("unsupported credentials type") — fall back to
	// the id_token carried on the user refresh grant.
	creds, err := google.FindDefaultCredentials(ctx, "openid", "email")
	if err != nil {
		return nil, err
	}
	ts := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	return fromTokenSource(ts, func(t *oauth2.Token) string {
		id, _ := t.Extra("id_token").(string)
		return id
	}), nil
}

// SourceTimeout builds Source but bounds credential discovery — which may
// include an eager network token fetch (idtoken.NewTokenSource fetches once at
// construction) — by timeout. The source itself is deliberately built on a
// background context: the oauth2 machinery binds the construction context into
// every future refresh, so cancelling it would poison the returned TokenFunc.
// On timeout the discovery goroutine is abandoned.
func SourceTimeout(timeout time.Duration, audience string) (TokenFunc, error) {
	type result struct {
		fn  TokenFunc
		err error
	}
	ch := make(chan result, 1)
	go func() {
		fn, err := Source(context.Background(), audience)
		ch <- result{fn, err}
	}()
	select {
	case r := <-ch:
		return r.fn, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("google credential discovery timed out after %s", timeout)
	}
}

// fromTokenSource adapts an oauth2.TokenSource, extracting the ID token from
// each refreshed token via pick.
func fromTokenSource(ts oauth2.TokenSource, pick func(*oauth2.Token) string) TokenFunc {
	return func(ctx context.Context) (string, error) {
		t, err := tokenWithContext(ctx, ts)
		if err != nil {
			return "", err
		}
		id := pick(t)
		if id == "" {
			return "", errors.New("credentials carry no ID token (re-run `gcloud auth application-default login`)")
		}
		return id, nil
	}
}

// tokenWithContext bounds ts.Token() — which takes no context and may hit the
// network (metadata server, oauth2.googleapis.com) — by ctx, so a hung
// credential endpoint cannot stall the caller past its deadline. On expiry the
// fetch goroutine is abandoned and finishes in the background (the source
// caches its result, so the work is not wasted).
func tokenWithContext(ctx context.Context, ts oauth2.TokenSource) (*oauth2.Token, error) {
	type result struct {
		t   *oauth2.Token
		err error
	}
	ch := make(chan result, 1)
	go func() {
		t, err := ts.Token()
		ch <- result{t, err}
	}()
	select {
	case r := <-ch:
		return r.t, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
