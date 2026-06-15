package reconcile

// Step is the one pure front door. Given the observed World and an incoming
// Signal it returns the new ChangeSet and the minimal Actions the shell must
// execute. Deterministic; no I/O; safe to call repeatedly.
func Step(w World, s Signal) (ChangeSet, []Action) {
	cs := w.Prior
	switch sig := s.(type) {
	case ApplySucceeded:
		return stepApplySucceeded(cs)
	default:
		_ = sig
		return cs, nil
	}
}

// stepApplySucceeded revokes the changeset's grants post-apply and marks the
// gate terminally Clean (privilege no longer needed).
func stepApplySucceeded(cs ChangeSet) (ChangeSet, []Action) {
	var actions []Action
	switch g := cs.Gate.(type) {
	case Pending:
		actions = revokeAll(cs, g.Targets)
	case Satisfied:
		actions = revokeAll(cs, g.Targets)
	case Blocked:
		actions = revokeAll(cs, g.Targets)
	default:
		return cs, nil
	}
	cs.Gate = Clean{}
	return cs, actions
}

// revokeAll emits a RevokeGrant for every target that still has a grant.
func revokeAll(cs ChangeSet, targets []Target) []Action {
	var actions []Action
	for _, t := range targets {
		if t.GrantName == "" {
			continue
		}
		actions = append(actions, RevokeGrant{
			Class: t.Class, Target: t.Target, PR: cs.PR, Environment: cs.Environment,
		})
	}
	return actions
}
