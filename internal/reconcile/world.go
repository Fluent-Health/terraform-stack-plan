package reconcile

// World is the scoped, immutable snapshot the shell hands to Step/Decide. Prior
// is the current persisted state for (PR, Environment); Version is the event
// stream's current version (0 for an empty stream), used for optimistic-
// concurrency append. Observations ride on the Signal. The core performs no I/O.
type World struct {
	Prior   ChangeSet
	Version int
}
