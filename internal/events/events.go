// Package events defines the typed, versioned protocol the CI runner speaks to
// the server: the lifecycle of a multi-stack Terraform execution
// (init → phase → per-stack updates → finalize) plus the apply-time gate
// exchange. One module, one version — the runner baked into a CI image and the
// deployed server share these types, so they can never disagree about event
// shapes.
package events

import (
	"encoding/json"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/codes"
	"github.com/Fluent-Health/terraform-stack-plan/internal/domain"
)

// Version is the protocol version. Bump on any breaking change to the payloads
// below; runner and server are expected to share the same module version.
const Version = 1

// Status is the lifecycle of a single stack within an execution.
type Status string

const (
	StatusPending      Status = "pending"      // intends to run
	StatusInitializing Status = "initializing" // terraform init running for this stack
	StatusInitialized  Status = "initialized"  // init done, awaiting plan
	StatusRunning      Status = "running"      // running now
	StatusPlanned      Status = "planned"      // plan done, no gate required
	StatusGated        Status = "gated"        // blocked on an approval gate
	StatusSafe         Status = "safe"         // approved / applied with changes / clean
	StatusNochange     Status = "nochange"     // applied, terraform reported 0 changes
	StatusMoving       Status = "moving"       // adopting resources via a cross-state move (non-gating)
	StatusFailed       Status = "failed"       // this stack errored
	StatusAborted      Status = "aborted"      // run failed elsewhere; this stack never reached a terminal tick
)

// AllStatuses lists every known stack status. Retained as public API: the test
// suite validates all members via this, and docs/consumers enumerate statuses from it.
func AllStatuses() []Status {
	return []Status{
		StatusPending, StatusInitializing, StatusInitialized, StatusRunning,
		StatusPlanned, StatusGated, StatusSafe, StatusNochange, StatusMoving,
		StatusFailed, StatusAborted,
	}
}

// Valid reports whether s is a known stack status. The empty status (zero
// value / unset) is treated as valid so omitted fields decode cleanly.
func (s Status) Valid() bool {
	switch s {
	case "", StatusPending, StatusInitializing, StatusInitialized, StatusRunning,
		StatusPlanned, StatusGated, StatusSafe, StatusNochange, StatusMoving,
		StatusFailed, StatusAborted:
		return true
	}
	return false
}

// ParseStatus validates a raw wire string into a Status, returning a coded
// (WIRE-001) error for an unknown non-empty value.
func ParseStatus(raw string) (Status, error) {
	s := Status(raw)
	if !s.Valid() {
		return "", codes.Errorf(codes.UnknownStatus, "unknown stack status %q", raw)
	}
	return s, nil
}

// UnmarshalJSON decodes a JSON string and validates it, so an unknown status in
// an untrusted payload fails fast at the wire boundary rather than entering the
// system silently. (The store does not decode status via JSON — it scans the
// column directly — so persisted values are never re-validated here.)
func (s *Status) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	v, err := ParseStatus(raw)
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Phase is the execution-wide lifecycle phase, narrated before and across the
// per-stack work.
type Phase string

const (
	PhaseWarming      Phase = "warming"      // warming the provider plugin cache
	PhaseLinting      Phase = "linting"      // static lint (e.g. tflint) over modules
	PhaseInitializing Phase = "initializing" // sequential terraform init
	PhasePlanning     Phase = "planning"     // parallel plan
	PhaseApplying     Phase = "applying"     // sequential apply
	PhaseTesting      Phase = "testing"      // post-apply contract tests
	PhaseVerifying    Phase = "verifying"    // post-apply verification
)

// Ticking reports whether a phase advances per-stack (k/N sub-progress) rather
// than being an instantaneous marker. The progress bar sub-fills a ticking
// phase's weighted band by completed/total; marker phases jump to their band.
func (p Phase) Ticking() bool {
	switch p {
	case PhaseInitializing, PhasePlanning, PhaseApplying:
		return true
	}
	return false
}

// Valid reports whether p is a known lifecycle phase.
func (p Phase) Valid() bool {
	switch p {
	case PhaseWarming, PhaseLinting, PhaseInitializing, PhasePlanning,
		PhaseApplying, PhaseTesting, PhaseVerifying:
		return true
	}
	return false
}

// Category is a classification label matched by a stack. Alias of the canonical
// domain type (carries Name/Icon and, additively, emitted Attributes).
type Category = domain.Category

// Counts is a stack's per-kind operation tally. Alias of the canonical domain
// type so the runner and server share one definition with the render pipeline.
type Counts = domain.Counts

// StackState is one node in the execution graph.
type StackState struct {
	Path       string     `json:"path"`
	Project    string     `json:"project,omitempty"` // grouping/target key (e.g. cloud project/account)
	Status     Status     `json:"status,omitempty"`
	Detail     string     `json:"detail,omitempty"`     // optional failure detail (last error lines)
	Categories []Category `json:"categories,omitempty"` // matched classification categories
	Counts     *Counts    `json:"counts,omitempty"`     // per-kind op tally (nil until finalize)
}

// Edge is a dependency: From must run before To.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is the changed subgraph for one execution.
type Graph struct {
	Stacks []StackState `json:"stacks"`
	Edges  []Edge       `json:"edges"`
}

// GateTarget names one thing that must be approved: a classification class
// (e.g. "iam") against a target (e.g. a cloud project/account). Generalises the
// single implicit IAM/project gate into (class, target) pairs so multiple
// approval classes are accommodated from the start.
type GateTarget struct {
	Class  string `json:"class"`
	Target string `json:"target"`
}

// Init registers an execution and its changed subgraph.
type Init struct {
	ID          string       `json:"id"`
	Repo        string       `json:"repo"`
	SHA         string       `json:"sha"`
	PR          int          `json:"pr"`
	Environment string       `json:"environment"`
	LogURL      string       `json:"log_url"`           // CI log deep-link, shown on failures
	Context     string       `json:"context,omitempty"` // commit-status context this run drives
	Stacks      []StackState `json:"stacks"`
	Edges       []Edge       `json:"edges"`
}

// PhaseEvent narrates a lifecycle phase transition (may arrive before Init).
type PhaseEvent struct {
	ID          string `json:"id"`
	Repo        string `json:"repo,omitempty"`
	SHA         string `json:"sha,omitempty"`
	PR          int    `json:"pr,omitempty"`
	Environment string `json:"environment,omitempty"`
	LogURL      string `json:"log_url,omitempty"`
	Context     string `json:"context,omitempty"`
	Phase       Phase  `json:"phase"`
	Label       string `json:"label,omitempty"`
	ProgressPct *int   `json:"progress_pct,omitempty"`
}

// LogChunk is a slice of one stack's combined output, streamed during execution.
type LogChunk struct {
	ID    string `json:"id"`
	Stack string `json:"stack"`
	Data  string `json:"data"`
}

// Update ticks a single stack's status.
type Update struct {
	ID     string `json:"id"`
	Stack  string `json:"stack"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Finalize records the terminal plan state: the rendered report, per-stack
// target backfill, the gates that must be approved, and the moving/failed flags.
type Finalize struct {
	ID             string                `json:"id"`
	ReportMarkdown string                `json:"report_markdown"`
	Projects       map[string]string     `json:"projects,omitempty"`      // stack path → grouping/target key
	StackReports   map[string]string     `json:"stack_reports,omitempty"` // stack path → rendered plan section
	Gates          []GateTarget          `json:"gates,omitempty"`         // (class,target) pairs needing approval
	Moving         []string              `json:"moving,omitempty"`        // stack paths adopting resources via a cross-state move
	Failed         bool                  `json:"failed,omitempty"`
	Categories     map[string][]Category `json:"categories,omitempty"` // stack path → matched categories
	Counts         map[string]Counts     `json:"counts,omitempty"`     // stack path → op counts
}

// GateCheck is the apply-time gate pre-check (fail-closed): is every required
// gate for this (PR, environment) approved?
type GateCheck struct {
	PR          int    `json:"pr"`
	Environment string `json:"environment"`
}

// GateRevoke asks the server to revoke the grants it requested for this
// (PR, environment) — best-effort post-apply cleanup.
type GateRevoke struct {
	PR          int    `json:"pr"`
	Environment string `json:"environment"`
}

// Claim is one apply-lock claim row, mirroring store.Claim over the wire for
// the /api/claims/list endpoint.
type Claim struct {
	Environment string    `json:"environment"`
	StackPath   string    `json:"stack_path"`
	OwnerPR     int       `json:"owner_pr"`
	ExpiresAt   time.Time `json:"expires_at"`
}
