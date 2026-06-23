package reconcile

// React projects the effects (Actions) the shell must perform from the domain
// facts. Presentation (RenderCheckRun/PublishSSE) is derived here, never stored
// as an Event. Action ordering must match step_table_test.go exactly.
func React(state ChangeSet, evs []Event) []Action {
	var actions []Action
	present := false // any presentation-bearing event seen
	for _, e := range evs {
		switch e.(type) {
		case ExecutionStarted, PhaseChanged, StackStatusChanged:
			present = true
		}
	}
	if present {
		actions = append(actions, RenderCheckRun{}, PublishSSE{})
	}
	return actions
}
