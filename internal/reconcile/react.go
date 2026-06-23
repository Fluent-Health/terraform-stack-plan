package reconcile

// React projects the effects (Actions) the shell must perform from the domain
// facts. Presentation (RenderCheckRun/PublishSSE) is derived here, never stored
// as an Event. Action ordering must match step_table_test.go exactly.
func React(state ChangeSet, evs []Event) []Action {
	var actions []Action
	present := false // any presentation-bearing event seen
	sseOnly := false // PR-closed path: SSE without RenderCheckRun
	for _, e := range evs {
		switch ev := e.(type) {
		case ExecutionStarted, PhaseChanged, StackStatusChanged:
			present = true
		case ClaimReleased:
			actions = append(actions, ReleaseClaim{PR: ev.PR, Environment: ev.Environment})
		case TargetRevoked:
			actions = append(actions, RevokeGrant{Class: ev.Class, Target: ev.Target, PR: ev.PR, Environment: ev.Env})
		case PRClosedRecorded:
			sseOnly = true
		}
	}
	switch {
	case sseOnly:
		actions = append(actions, PublishSSE{})
	case present:
		actions = append(actions, RenderCheckRun{}, PublishSSE{})
	}
	return actions
}
