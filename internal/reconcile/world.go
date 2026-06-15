package reconcile

// World is the scoped, immutable snapshot the shell hands to Step. The prior
// ChangeSet is the current persisted state for (PR, Environment); observations
// ride on the Signal. The core performs no I/O.
type World struct {
	Prior ChangeSet
}
