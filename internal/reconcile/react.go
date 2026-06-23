package reconcile

// React projects the effects (Actions) the shell must perform from the domain
// facts. Presentation (RenderCheckRun/PublishSSE) is derived here, never stored
// as an Event. Action ordering must match step_table_test.go exactly.
func React(state ChangeSet, evs []Event) []Action {
	var actions []Action

	// Presentation precedence: 0=none, 1=in-progress, 2=success, 3=failure.
	// (action_required outcomes are TERMINAL and ride precedence 2.) Higher
	// precedence wins; exactly one RenderCheckRun+PublishSSE per Step.
	renderPrec := 0
	var renderAction RenderCheckRun
	sseOnly := false // PR-closed path: SSE without RenderCheckRun

	// Observe-path projection: a GrantObserved/GrantCleared batch with NO outcome
	// event (GateSatisfied / GateBlocked / GateTargetRequested) is the settled
	// awaiting-approval fallthrough — it stays Pending but renders TERMINAL
	// action_required, derived below from the post-fold state.Gate. (The state is
	// itself derived from these facts, so this is a legitimate CQRS projection.)
	observeBatch := false
	gateOutcome := false

	for _, e := range evs {
		switch ev := e.(type) {
		case ExecutionStarted, PhaseChanged, StackStatusChanged:
			// Non-terminal in-progress render (precedence 1).
			if renderPrec < 1 {
				renderPrec = 1
				renderAction = RenderCheckRun{}
			}
		case ExecutionFailed:
			// Terminal failure render (precedence 3 — highest).
			if renderPrec < 3 {
				renderPrec = 3
				renderAction = RenderCheckRun{Terminal: true, Conclusion: "failure"}
			}
		case GatePassed:
			// Terminal success render (precedence 2).
			if renderPrec < 2 {
				renderPrec = 2
				renderAction = RenderCheckRun{Terminal: true, Conclusion: "success"}
			}
		case Classified:
			// Gated outcome always renders (non-terminal in-progress, precedence 1).
			if renderPrec < 1 {
				renderPrec = 1
				renderAction = RenderCheckRun{}
			}
		case GateTargetRequested:
			// Emit RequestGrant. Also a non-terminal in-progress render (precedence 1).
			gateOutcome = true
			actions = append(actions, RequestGrant{
				Class:     ev.Class,
				Target:    ev.Target,
				Requester: ev.Requester,
			})
			if renderPrec < 1 {
				renderPrec = 1
				renderAction = RenderCheckRun{}
			}
		case GrantObserved, GrantCleared:
			// Fold-only facts: no presentation. Mark the batch so the settled
			// awaiting-approval fallthrough can be projected after the loop.
			observeBatch = true
		case GateSatisfied:
			// Terminal success (precedence 2).
			gateOutcome = true
			if renderPrec < 2 {
				renderPrec = 2
				renderAction = RenderCheckRun{Terminal: true, Conclusion: "success"}
			}
		case GateBlocked:
			// Terminal action_required for denied/revoked/expired; NON-terminal for
			// slot collisions (slot_self/slot_foreign), where the PR keeps waiting.
			gateOutcome = true
			if ev.Reason == ReasonSlotSelf || ev.Reason == ReasonSlotForeign {
				if renderPrec < 1 {
					renderPrec = 1
					renderAction = RenderCheckRun{}
				}
			} else if renderPrec < 2 {
				renderPrec = 2
				renderAction = RenderCheckRun{Terminal: true, Conclusion: "action_required"}
			}
		case ClaimReleased:
			actions = append(actions, ReleaseClaim{PR: ev.PR, Environment: ev.Environment})
		case TargetRevoked:
			actions = append(actions, RevokeGrant{Class: ev.Class, Target: ev.Target, PR: ev.PR, Environment: ev.Env})
		case PRClosedRecorded:
			sseOnly = true
		}
	}
	// Settled awaiting-approval fallthrough: an observe batch with no gate outcome
	// that left the gate Pending renders TERMINAL action_required (step.go:346-347).
	if observeBatch && !gateOutcome {
		if _, pending := state.Gate.(Pending); pending && renderPrec < 2 {
			renderPrec = 2
			renderAction = RenderCheckRun{Terminal: true, Conclusion: "action_required"}
		}
	}

	switch {
	case sseOnly:
		actions = append(actions, PublishSSE{})
	case renderPrec > 0:
		actions = append(actions, renderAction, PublishSSE{})
	}
	return actions
}
