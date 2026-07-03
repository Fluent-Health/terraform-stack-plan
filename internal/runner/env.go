package runner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
)

// Environment variables the orchestrator (`run plan`/`run apply`) sets and the
// per-stack `run tick` reads, so progress reporting needs no flags threaded
// through the terramate scripts. Empty TFSTACKPLAN_SERVER disables reporting.
const (
	EnvServer      = "TFSTACKPLAN_SERVER"      // control-plane base URL ("" = offline, no-op)
	EnvToken       = "TFSTACKPLAN_TOKEN"       // legacy shared bearer secret for /api/* (unset = OIDC via ADC)
	EnvAudience    = "TFSTACKPLAN_AUDIENCE"    // OIDC ID-token audience (default: the server URL)
	EnvExecution   = "TFSTACKPLAN_EXECUTION"   // execution id this run reports under
	EnvStack       = "TFSTACKPLAN_STACK"       // current stack path (fallback for `run tick --stack`)
	EnvEnvironment = "TFSTACKPLAN_ENVIRONMENT" // deployment environment for the execution
)

// ClientFromEnv builds a Client from TFSTACKPLAN_SERVER. When the server var is
// empty the client is disabled (every call is a no-op). Auth: the legacy shared
// secret when TFSTACKPLAN_TOKEN is set; otherwise Google OIDC ID tokens from
// Application Default Credentials, with audience TFSTACKPLAN_AUDIENCE (default:
// the server URL). No ADC → unauthenticated (best-effort calls degrade, the
// fail-closed gate check errors).
func ClientFromEnv() *Client {
	base := os.Getenv(EnvServer)
	if base == "" {
		return NewClient("", "")
	}
	if secret := os.Getenv(EnvToken); secret != "" {
		return NewClient(base, secret)
	}
	aud := os.Getenv(EnvAudience)
	if aud == "" {
		aud = strings.TrimRight(base, "/")
	}
	src, err := gauth.Source(context.Background(), aud)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tfstackplan: no API credentials (%s unset and no Google ADC: %v) — reporting unauthenticated\n", EnvToken, err)
		return NewClient(base, "")
	}
	return NewClientTokenSource(base, src)
}
