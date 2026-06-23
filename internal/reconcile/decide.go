package reconcile

import "sort"

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
	case RunnerFinalize:
		return decideFinalize(state, sig)
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

// decideFinalize implements the three RunnerFinalize outcomes:
//   - failed: emit ExecutionFailed (Evolve fails-open stacks).
//   - clean (effective gate empty): emit StacksClassified + GatePassed.
//   - gated: emit StacksClassified + Classified{effective} + TargetRevoked for
//     each pruned dropped target (plan-authoritative only) + GateTargetRequested
//     for the first target lacking a carried-forward live grant.
//
// Mirrors the logic of stepFinalize (step.go:43-135) exactly.
func decideFinalize(state ChangeSet, f RunnerFinalize) []Event {
	if f.Failed {
		return []Event{ExecutionFailed{}}
	}

	evs := []Event{StacksClassified{Projects: f.Projects, Categories: f.Categories}}

	prior := priorTargets(state.Gate)

	// Effective gate set: plan-authoritative replaces; apply-context unions.
	gates := f.Gates
	if f.ApplyContext && len(prior) > 0 {
		gates = unionPriorTargets(f.Gates, prior)
	}

	if len(gates) == 0 {
		return append(evs, GatePassed{})
	}
	evs = append(evs, Classified{Gates: gates})

	// Prune (revoke) dropped targets — authoritative plan finalize only.
	// Apply-context finalizes never prune (step.go:108-122).
	if !f.ApplyContext {
		want := make(map[string]bool, len(gates))
		for _, g := range gates {
			want[g.Class+"|"+g.Target] = true
		}
		for _, key := range sortedKeys(prior) {
			pt := prior[key]
			if !want[key] && pt.GrantName != "" {
				evs = append(evs, TargetRevoked{
					Class: pt.Class, Target: pt.Target,
					PR: state.PR, Env: state.Environment,
				})
			}
		}
	}

	// Request the first target with no carried-forward grant.
	// Mirror Evolve(Classified)'s carry-forward: if prior[key].Grant.Open()
	// the target will be carried forward (GrantName retained); otherwise it
	// starts fresh and must be requested (step.go:127-131, 88-101).
	lease := priorLease(state.Gate)
	for _, g := range gates {
		key := g.Class + "|" + g.Target
		if pt, ok := prior[key]; ok && pt.Grant.Open() {
			continue // carried forward — no request needed
		}
		evs = append(evs, GateTargetRequested{
			Class:     g.Class,
			Target:    g.Target,
			Requester: lease.Requester,
		})
		break
	}
	return evs
}

// sortedKeys returns the keys of m in sorted order (deterministic output).
func sortedKeys(m map[string]Target) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
