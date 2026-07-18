// Package execution is the pure event-sourced domain core for one execution's
// lifecycle (init → phase → per-stack ticks → terminal). It follows the Decider
// pattern: Decide emits Events, Evolve folds them into a State. No I/O, no store
// imports — a pure leaf package, mirroring internal/claims. The aggregate is
// scoped per execution id (stream "run:<execID>"); it carries NO gate state (the
// gate lives in the (pr,env) aggregate in internal/reconcile).
package execution

import "github.com/Fluent-Health/terraform-stack-plan/internal/events"

// State is the folded lifecycle of ONE execution.
type State struct {
	ID            string
	Repo          string
	SHA           string
	LogURL        string
	PR            int
	Environment   string
	Context       string
	Phase         events.Phase
	ProgressLabel string
	ProgressPct   *int
	Status        string
	Stacks        []Stack
	Edges         []Edge
}

// Stack is one node. RunStatus holds ONLY the runner-told status; the gated/safe
// overlay is derived elsewhere (the (pr,env) gate aggregate's projection).
type Stack struct {
	Path       string
	Project    string
	RunStatus  events.Status
	Detail     string
	Categories []events.Category
	Counts     *events.Counts
}

// Edge is a directed edge between two stacks.
type Edge struct {
	From string
	To   string
}

// Empty is the initial state for the fold (an unseen execution stream).
func Empty() State { return State{} }

// --- Signal sum type (told-facts from the CI runner) ---

// Signal is the union of everything that can drive an execution's State.
type Signal interface{ isSignal() }

// ReportInit registers the execution and its changed subgraph.
type ReportInit struct{ Exec State }

// ReportPhase narrates a lifecycle phase transition.
type ReportPhase struct {
	Phase       events.Phase
	Label       string
	Pct         *int
	ID          string
	PR          int
	Repo        string
	SHA         string
	Environment string
	Context     string
	LogURL      string
}

// ReportTick ticks a single stack's runner-told status.
type ReportTick struct {
	Stack  string
	Status events.Status
	Detail string
}

// ReportFail marks the run terminally failed (fails-open innocent stacks in Evolve).
type ReportFail struct{}

// ReportSucceed marks the run terminally succeeded.
type ReportSucceed struct{}

// ReportAnnotate annotates stacks with project, categories, counts, and moving state.
type ReportAnnotate struct {
	Projects   map[string]string
	Categories map[string][]events.Category
	Counts     map[string]events.Counts
	Moving     []string
}

func (ReportInit) isSignal()     {}
func (ReportPhase) isSignal()    {}
func (ReportTick) isSignal()     {}
func (ReportFail) isSignal()     {}
func (ReportSucceed) isSignal()  {}
func (ReportAnnotate) isSignal() {}

// --- Event sum type (past-tense domain facts) ---

// Event is a past-tense domain fact produced by Decide and folded by Evolve.
type Event interface{ isEvent() }

type Started struct{ Exec State }
type PhaseChanged struct {
	Phase       events.Phase
	Label       string
	Pct         *int
	ID          string
	PR          int
	Repo        string
	SHA         string
	Environment string
	Context     string
	LogURL      string
}
type StackStatusChanged struct {
	Stack  string
	Status events.Status
	Detail string
}
type Failed struct{}
type Succeeded struct{}
type StacksAnnotated struct {
	Projects   map[string]string
	Categories map[string][]events.Category
	Counts     map[string]events.Counts
	Moving     []string
}

func (Started) isEvent()            {}
func (PhaseChanged) isEvent()       {}
func (StackStatusChanged) isEvent() {}
func (Failed) isEvent()             {}
func (Succeeded) isEvent()          {}
func (StacksAnnotated) isEvent()    {}
