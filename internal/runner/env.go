package runner

import "os"

// Environment variables the orchestrator (`run plan`/`run apply`) sets and the
// per-stack `run tick` reads, so progress reporting needs no flags threaded
// through the terramate scripts. Empty TFSTACKPLAN_SERVER disables reporting.
const (
	EnvServer    = "TFSTACKPLAN_SERVER"    // control-plane base URL ("" = offline, no-op)
	EnvToken     = "TFSTACKPLAN_TOKEN"     // bearer secret for /api/*
	EnvExecution = "TFSTACKPLAN_EXECUTION" // execution id this run reports under
	EnvStack     = "TFSTACKPLAN_STACK"     // current stack path (fallback for `run tick --stack`)
)

// ClientFromEnv builds a Client from TFSTACKPLAN_SERVER + TFSTACKPLAN_TOKEN. When
// the server var is empty the client is disabled (every call is a no-op).
func ClientFromEnv() *Client {
	return NewClient(os.Getenv(EnvServer), os.Getenv(EnvToken))
}
