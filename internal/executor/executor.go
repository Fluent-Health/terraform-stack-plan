// Package executor abstracts the CI muscle the control plane drives: serve
// receives the webhooks and asks a Backend to start/cancel/probe builds. Only
// cloudbuild is implemented; other shapes (github-actions workflow_dispatch,
// gitlab, generic webhook) are documented futures behind the same seam.
package executor

import "context"

// RunRequest describes one CI run to start.
type RunRequest struct {
	Kind        string // "plan" | "apply" (selects the backend trigger)
	Environment string
	SHA         string // exact commit to build
	Branch      string // branch ref for backends that need one
	ExecutionID string // serve-minted execution id the runner must report under
	PR          int
}

// Ref identifies a started run at the backend (e.g. a Cloud Build build id).
type Ref struct {
	ID string
}

// Phase is the backend's view of a run, for the start watchdog.
type Phase string

const (
	PhaseQueued   Phase = "queued"   // accepted, not yet provisioned
	PhaseWorking  Phase = "working"  // machine running
	PhaseDone     Phase = "done"     // finished successfully
	PhaseFailed   Phase = "failed"   // finished unsuccessfully (incl. cancelled)
	PhaseNotFound Phase = "notfound" // backend has no such run
)

// Backend starts and manages CI runs.
type Backend interface {
	// Start begins a run and returns its backend reference.
	Start(ctx context.Context, req RunRequest) (Ref, error)
	// Cancel stops an in-flight run. Idempotent; cancelling a finished run is
	// not an error.
	Cancel(ctx context.Context, ref Ref) error
	// Probe reports the run's current phase — used by the start watchdog to
	// distinguish "still provisioning" from "never started".
	Probe(ctx context.Context, ref Ref) (Phase, error)
}
