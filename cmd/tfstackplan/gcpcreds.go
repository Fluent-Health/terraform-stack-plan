package main

import (
	"context"
	"fmt"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/iamcredentials/v1"

	"github.com/Fluent-Health/terraform-stack-plan/internal/approval/gcppam"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// mintAccessToken returns a short-lived OAuth access token for sa via the IAM
// Credentials generateAccessToken API, using the process ADC (which must hold
// serviceAccountTokenCreator on sa). Swappable in tests.
var mintAccessToken = realMintAccessToken

func realMintAccessToken(ctx context.Context, sa string) (string, error) {
	svc, err := iamcredentials.NewService(ctx)
	if err != nil {
		return "", fmt.Errorf("iamcredentials service: %w", err)
	}
	name := "projects/-/serviceAccounts/" + sa
	resp, err := svc.Projects.ServiceAccounts.GenerateAccessToken(name, &iamcredentials.GenerateAccessTokenRequest{
		Scope: []string{cloudPlatformScope},
	}).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("mint access token for %s: %w", sa, err)
	}
	return resp.AccessToken, nil
}

// gcpCreds returns the ADC token func (for PAM list/revoke) and the SA
// impersonation func (for PAM create), using Application Default Credentials.
// The runtime identity needs PAM viewer + a revoke role on the targets, and
// serviceAccountTokenCreator on the requester pool SAs.
func gcpCreds(ctx context.Context) (gcppam.TokenFunc, gcppam.ImpersonateFunc, error) {
	creds, err := google.FindDefaultCredentials(ctx, cloudPlatformScope)
	if err != nil {
		return nil, nil, fmt.Errorf("application default credentials: %w", err)
	}
	token := func(ctx context.Context) (string, error) {
		t, err := creds.TokenSource.Token()
		if err != nil {
			return "", err
		}
		return t.AccessToken, nil
	}

	svc, err := iamcredentials.NewService(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("iamcredentials service: %w", err)
	}
	impersonate := func(ctx context.Context, serviceAccount string) (string, error) {
		name := "projects/-/serviceAccounts/" + serviceAccount
		resp, err := svc.Projects.ServiceAccounts.GenerateAccessToken(name, &iamcredentials.GenerateAccessTokenRequest{
			Scope: []string{cloudPlatformScope},
		}).Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("impersonate %s: %w", serviceAccount, err)
		}
		return resp.AccessToken, nil
	}
	return token, impersonate, nil
}
