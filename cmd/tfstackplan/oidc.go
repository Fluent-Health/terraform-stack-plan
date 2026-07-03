package main

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/api/idtoken"
)

// gcpOIDCVerifier verifies a Google-signed OIDC bearer token for the given
// audience and returns the token's email claim. Used for Pub/Sub push auth.
func gcpOIDCVerifier(audience string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, token string) (string, error) {
		payload, err := idtoken.Validate(ctx, token, audience)
		if err != nil {
			return "", err
		}
		email, _ := payload.Claims["email"].(string)
		if email == "" {
			return "", fmt.Errorf("oidc token has no email claim")
		}
		return email, nil
	}
}

// gcpAPIVerifier verifies a Google-signed OIDC bearer token for /api/* and
// returns its verified email claim. The audience is checked here against an
// allowlist (not inside idtoken.Validate) so user-ADC tokens — whose audience
// is the fixed gcloud client id, not the serve URL — can be accepted alongside
// service-account tokens minted for the serve URL.
func gcpAPIVerifier(audiences []string) func(context.Context, string) (string, error) {
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
