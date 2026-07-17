package execution

// Decide maps a Signal to the past-tense domain facts it produces. Pure: no I/O,
// no time.Now(). All business logic lives here; Evolve only folds.
func Decide(s State, sig Signal) []Event {
	switch v := sig.(type) {
	case ReportInit:
		return []Event{Started{Exec: v.Exec}}
	case ReportPhase:
		return []Event{PhaseChanged{
			Phase: v.Phase, Label: v.Label, Pct: v.Pct,
			ID: v.ID, PR: v.PR, Repo: v.Repo, SHA: v.SHA,
			Environment: v.Environment, Context: v.Context, LogURL: v.LogURL,
		}}
	case ReportTick:
		return []Event{StackStatusChanged{Stack: v.Stack, Status: v.Status, Detail: v.Detail}}
	case ReportFail:
		return []Event{Failed{}}
	case ReportSucceed:
		return []Event{Succeeded{}}
	default:
		return nil
	}
}
