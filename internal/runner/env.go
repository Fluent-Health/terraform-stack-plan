package runner

import (
	"fmt"
	"os"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
)

// Environment variables the orchestrator (`run plan`/`run apply`) sets and the
// per-stack `run tick` reads, so progress reporting needs no flags threaded
// through the terramate scripts. Empty TFSTACKPLAN_SERVER disables reporting.
const (
	EnvServer      = "TFSTACKPLAN_SERVER"      // control-plane base URL ("" = offline, no-op)
	EnvAudience    = "TFSTACKPLAN_AUDIENCE"    // OIDC ID-token audience; set = authenticate via Google ADC
	EnvExecution   = "TFSTACKPLAN_EXECUTION"   // execution id this run reports under
	EnvStack       = "TFSTACKPLAN_STACK"       // current stack path (fallback for `run tick --stack`)
	EnvEnvironment = "TFSTACKPLAN_ENVIRONMENT" // deployment environment for the execution
)

// APITokenFunc returns the /api/* bearer source: a non-empty audience selects
// Google OIDC ID tokens from Application Default Credentials — with discovery
// (which can hit the network) bounded to 10s; an empty audience → nil
// (unauthenticated). OIDC is deliberately opt-in via the audience: a
// token-less environment must not probe ambient machine credentials, hard-fail
// on a stale ADC file, or send a replayable ID token to whatever host the
// server URL happens to name.
func APITokenFunc(audience string) (gauth.TokenFunc, error) {
	if audience == "" {
		return nil, nil
	}
	return gauth.SourceTimeout(10*time.Second, audience)
}

// ClientFromEnv builds a Client from TFSTACKPLAN_SERVER. When the server var is
// empty the client is disabled (every call is a no-op). Auth comes from
// APITokenFunc over TFSTACKPLAN_AUDIENCE; when the audience is set but ADC is
// unavailable, a warning is printed and requests go unauthenticated
// (best-effort reporting degrades, the fail-closed gate check errors).
func ClientFromEnv() *Client {
	base := os.Getenv(EnvServer)
	if base == "" {
		return NewClient("")
	}
	tok, err := APITokenFunc(os.Getenv(EnvAudience))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tfstackplan: %s is set but Google ADC is unavailable (%v) — reporting unauthenticated\n", EnvAudience, err)
	}
	return NewClientTokenSource(base, tok)
}
