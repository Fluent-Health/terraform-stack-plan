// Package events defines the typed, versioned protocol the CI runner speaks to
// the server: the lifecycle of a multi-stack Terraform execution
// (init → phase → per-stack updates → finalize) plus the apply-time gate
// exchange. One module, one version — the runner baked into a CI image and the
// deployed server share these types, so they can never disagree about event
// shapes.
package events

// Version is the protocol version. Bump on any breaking change to the payloads
// below; runner and server are expected to share the same module version.
const Version = 1

// Status is the lifecycle of a single stack within an execution.
type Status string

const (
	StatusPending Status = "pending" // intends to run
	StatusRunning Status = "running" // running now
	StatusPlanned Status = "planned" // plan done, no gate required
	StatusGated   Status = "gated"   // blocked on an approval gate
	StatusSafe    Status = "safe"    // approved / applied / clean
	StatusMoving  Status = "moving"  // adopting resources via a cross-state move (non-gating)
	StatusFailed  Status = "failed"
)

// Phase is the execution-wide lifecycle phase, narrated before and across the
// per-stack work.
type Phase string

const (
	PhaseWarming      Phase = "warming"      // warming the provider plugin cache
	PhaseInitializing Phase = "initializing" // sequential terraform init
	PhasePlanning     Phase = "planning"     // parallel plan
	PhaseApplying     Phase = "applying"     // sequential apply
	PhaseVerifying    Phase = "verifying"    // post-apply verification
)

// Category is a classification label matched by a stack (name + optional glyph).
type Category struct {
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// Counts is a stack's per-kind operation tally, for the blast-radius bar and the
// op-count summaries. Mirrors internal/model.Counts but lives in the protocol
// package so the runner and server share the wire shape without importing
// internal/model. All fields omitempty so a zero stack marshals compactly.
type Counts struct {
	Add     int `json:"add,omitempty"`
	Change  int `json:"change,omitempty"`
	Destroy int `json:"destroy,omitempty"`
	Replace int `json:"replace,omitempty"`
	Move    int `json:"move,omitempty"`
	Import  int `json:"import,omitempty"`
	Forget  int `json:"forget,omitempty"`
}

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
