package reconcile

// Decide computes the past-tense domain facts that result from applying Signal
// s to the prior state. All business logic lives here; Evolve only folds.
func Decide(state ChangeSet, s Signal) []Event {
	switch sig := s.(type) {
	case RunnerInit:
		return []Event{ExecutionStarted{Exec: sig.Exec}}
	case RunnerPhase:
		return []Event{PhaseChanged{Phase: sig.Phase}}
	case RunnerUpdate:
		return []Event{StackStatusChanged{Stack: sig.Stack, Status: sig.Status, Detail: sig.Detail}}
	case ApplySucceeded:
		if _, ok := state.Gate.(NotClassified); ok {
			return nil
		}
		evs := []Event{ClaimReleased{PR: state.PR, Environment: state.Environment}}
		for _, t := range gateTargets(state.Gate) {
			if t.GrantName == "" {
				continue
			}
			evs = append(evs, TargetRevoked{Class: t.Class, Target: t.Target, PR: state.PR, Env: state.Environment})
		}
		return append(evs, GateReleased{})
	case PRClosed:
		targets := gateTargets(state.Gate)
		var revokes []Event
		for _, t := range targets {
			if t.GrantName == "" {
				continue
			}
			revokes = append(revokes, TargetRevoked{Class: t.Class, Target: t.Target, PR: state.PR, Env: state.Environment})
		}
		if len(revokes) == 0 {
			return nil
		}
		return append(revokes, PRClosedRecorded{}, GateBlocked{Reason: ReasonRevoked})
	default:
		return nil
	}
}

// runDecider is the Decider orchestrator: Decide → fold via Evolve → React.
func runDecider(prior ChangeSet, s Signal) (ChangeSet, []Action) {
	evs := Decide(prior, s)
	st := prior
	for _, e := range evs {
		st = Evolve(st, e)
	}
	return st, React(st, evs)
}
