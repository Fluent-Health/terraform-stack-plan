package claims

// Decide maps a Command to the set of Events it produces. It is pure: no I/O,
// no time.Now() — all temporal inputs arrive via the command itself. The caller
// (the shell) is responsible for providing the current time inside Now fields.
func Decide(s ClaimSet, c Command) []Event {
	switch cmd := c.(type) {
	case AcquireClaim:
		return []Event{ClaimAcquired{
			PR:        cmd.PR,
			Stacks:    cmd.Stacks,
			ExpiresAt: cmd.Now.Add(Lease()),
		}}
	case RenewClaim:
		return []Event{ClaimRenewed{
			PR:        cmd.PR,
			ExpiresAt: cmd.Now.Add(Lease()),
		}}
	case ReleaseClaim:
		return []Event{ClaimReleased{PR: cmd.PR}}
	case ReleaseClaimStack:
		return []Event{ClaimStackReleased{PR: cmd.PR, Stack: cmd.Stack}}
	default:
		return nil
	}
}
