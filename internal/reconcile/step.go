package reconcile

// Step is the one pure front door. Decide computes the domain facts an incoming
// Signal produces against the observed World; Evolve folds them into the new
// ChangeSet (the replay path); React projects the Actions the shell must run.
// Deterministic; no I/O; safe to call repeatedly.
func Step(w World, s Signal) (ChangeSet, []Action) {
	evs := Decide(w.Prior, s)
	st := w.Prior
	for _, e := range evs {
		st = Evolve(st, e)
	}
	return st, React(st, evs)
}
