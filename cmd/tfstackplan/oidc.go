package main

import (
	"context"
	"fmt"

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
