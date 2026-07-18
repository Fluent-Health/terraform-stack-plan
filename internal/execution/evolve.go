package execution

import "github.com/Fluent-Health/terraform-stack-plan/internal/events"

// Evolve applies a single domain Event to a State, returning the new state. Total
// (never panics on any state/event combination), pure, deterministic — the fold
// half of the Decider pattern.
func Evolve(s State, e Event) State {
	switch ev := e.(type) {
	case Started:
		// A fresh Init replaces the whole execution state (identity/repo/sha/phase/
		// status/edges come from the new Init) EXCEPT per-stack runner progress,
		// which must be non-regressive — mirrors the old store.UpsertInit's stack
		// upsert (`ON CONFLICT(execution_id, stack_path) DO UPDATE SET
		// project=excluded.project`, i.e. only project is refreshed; status/detail/
		// categories/counts are set only on first insert). This preserves an
		// already-advanced stack (e.g. via `run register` → initializing/
		// initialized) across the later Init from `run plan`.
		prior := make(map[string]Stack, len(s.Stacks))
		for _, st := range s.Stacks {
			prior[st.Path] = st
		}
		// Build the merged stacks into a freshly allocated slice -- never write
		// through ev.Exec.Stacks elements, which would mutate the caller's Event
		// (shell.go re-persists the same event slice via Append after folding).
		merged := make([]Stack, 0, len(ev.Exec.Stacks)+len(s.Stacks))
		seen := make(map[string]bool, len(ev.Exec.Stacks))
		for _, st := range ev.Exec.Stacks {
			seen[st.Path] = true
			if old, ok := prior[st.Path]; ok {
				project := old.Project
				if st.Project != "" {
					project = st.Project
				}
				old.Project = project
				merged = append(merged, old)
			} else {
				merged = append(merged, st)
			}
		}
		// Carry forward prior stacks absent from the new Init (the old projection
		// never deleted stacks), preserving their prior relative order.
		for _, st := range s.Stacks {
			if !seen[st.Path] {
				merged = append(merged, st)
			}
		}
		result := ev.Exec
		result.Stacks = merged
		return result

	case PhaseChanged:
		s.Phase = ev.Phase
		s.ProgressLabel = ev.Label
		s.ProgressPct = ev.Pct
		// Identity is set non-regressively so a phase-before-init materializes the
		// row without a later bare phase bump clobbering it (mirrors old UpsertPhase).
		if s.ID == "" {
			s.ID = ev.ID
		}
		if s.PR == 0 {
			s.PR = ev.PR
		}
		if s.Repo == "" {
			s.Repo = ev.Repo
		}
		if s.SHA == "" {
			s.SHA = ev.SHA
		}
		if s.Environment == "" {
			s.Environment = ev.Environment
		}
		if s.Context == "" {
			s.Context = ev.Context
		}
		if s.LogURL == "" {
			s.LogURL = ev.LogURL
		}
		return s

	case StackStatusChanged:
		for i := range s.Stacks {
			if s.Stacks[i].Path == ev.Stack {
				s.Stacks[i].RunStatus = ev.Status
				s.Stacks[i].Detail = ev.Detail
			}
		}
		return s

	case Failed:
		s.Status = "failure"
		// A stack still mid-run at a failed finalize did not itself fail — terramate
		// aborted it (e.g. a parallel sibling 403'd). Mark it `aborted`, not `failed`,
		// so innocent / no-change stacks are not mislabeled (matches live handleFinalize).
		for i := range s.Stacks {
			switch s.Stacks[i].RunStatus {
			case events.StatusPending, events.StatusRunning,
				events.StatusInitializing, events.StatusInitialized,
				events.StatusMoving:
				s.Stacks[i].RunStatus = events.StatusAborted
			}
		}
		return s

	case Succeeded:
		s.Status = "success"
		return s

	case StacksAnnotated:
		moving := make(map[string]bool, len(ev.Moving))
		for _, p := range ev.Moving {
			moving[p] = true
		}
		for i := range s.Stacks {
			p := &s.Stacks[i]
			if proj, ok := ev.Projects[p.Path]; ok {
				p.Project = proj
			}
			if cats, ok := ev.Categories[p.Path]; ok {
				p.Categories = cats
			}
			if c, ok := ev.Counts[p.Path]; ok {
				cc := c
				p.Counts = &cc
			}
			// Moving overlays status for non-terminal stacks only (matches the old
			// finalize UPDATE ... WHERE status NOT IN (failed, aborted)).
			if moving[p.Path] && p.RunStatus != events.StatusFailed && p.RunStatus != events.StatusAborted {
				p.RunStatus = events.StatusMoving
			}
		}
		return s

	case Superseded:
		s.SupersededBy = ev.By
		return s

	default:
		return s
	}
}
