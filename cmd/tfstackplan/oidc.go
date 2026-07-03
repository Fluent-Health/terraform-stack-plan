package main

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/api/idtoken"
)

// gcpIDTokenVerifier verifies a Google-signed OIDC bearer token and returns
// its verified email claim. Used for Pub/Sub push auth (single audience) and
// /api/* auth (audience allowlist). The audience is checked here rather than
// inside idtoken.Validate so callers can accept multiple audiences — e.g.
// user-ADC tokens carrying the fixed gcloud client id alongside
// service-account tokens minted for the serve URL.
func gcpIDTokenVerifier(audiences []string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, token string) (string, error) {
		payload, err := idtoken.Validate(ctx, token, "")
		if err != nil {
			return "", err
		}
		if !slices.Contains(audiences, payload.Audience) {
			return "", fmt.Errorf("unexpected audience %q", payload.Audience)
		}
		email, _ := payload.Claims["email"].(string)
		if email == "" {
			return "", fmt.Errorf("oidc token has no email claim")
		}
		if verified, ok := payload.Claims["email_verified"].(bool); ok && !verified {
			return "", fmt.Errorf("oidc token email not verified")
		}
		return email, nil
	}
}
