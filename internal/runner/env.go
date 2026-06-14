package runner

import "os"

// Environment variables the orchestrator (`run plan`/`run apply`) sets and the
// per-stack `run tick` reads, so progress reporting needs no flags threaded
// through the terramate scripts. Empty TFSTACKPLAN_SERVER disables reporting.
const (
	EnvServer      = "TFSTACKPLAN_SERVER"      // control-plane base URL ("" = offline, no-op)
	EnvToken       = "TFSTACKPLAN_TOKEN"       // bearer secret for /api/*
	EnvIAPAudience = "TFSTACKPLAN_IAP_AUDIENCE" // IAP OAuth2 client ID ("" = no IAP)
	EnvExecution   = "TFSTACKPLAN_EXECUTION"   // execution id this run reports under
	EnvStack       = "TFSTACKPLAN_STACK"       // current stack path (fallback for `run tick --stack`)
	EnvEnvironment = "TFSTACKPLAN_ENVIRONMENT" // deployment environment for the execution
)

// ClientFromEnv builds a Client from TFSTACKPLAN_SERVER + TFSTACKPLAN_TOKEN. When
// the server var is empty the client is disabled (every call is a no-op).
// If TFSTACKPLAN_IAP_AUDIENCE is set the client fetches a GCP IAP OIDC token and
// sends it as the Authorization bearer; the webhook secret moves to X-Tfstackplan-Token.
func ClientFromEnv() *Client {
	c := NewClient(os.Getenv(EnvServer), os.Getenv(EnvToken))
	if aud := os.Getenv(EnvIAPAudience); aud != "" {
		c.iapAudience = aud
	}
	return c
}
