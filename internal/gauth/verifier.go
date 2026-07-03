package gauth

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

// VerifyFunc verifies a Google-signed OIDC bearer token and returns its
// verified email claim. This is the server-side counterpart of TokenFunc.
type VerifyFunc func(ctx context.Context, token string) (email string, err error)

// Verifier builds a VerifyFunc accepting tokens for any of audiences. Used for
// Pub/Sub push auth (single audience) and /api/* auth (audience allowlist).
// The audience is checked here rather than inside idtoken's validation so
// callers can accept multiple audiences — e.g. user-ADC tokens carrying the
// fixed gcloud client id alongside service-account tokens minted for the
// serve URL. opts configure the validator's key-fetching HTTP client; tests
// inject one serving their own JWKS.
func Verifier(ctx context.Context, audiences []string, opts ...option.ClientOption) (VerifyFunc, error) {
	v, err := idtoken.NewValidator(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, token string) (string, error) {
		payload, err := v.Validate(ctx, token, "")
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
	}, nil
}
