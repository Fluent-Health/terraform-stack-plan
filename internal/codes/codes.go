// Package codes is the closed registry of stable, machine-readable problem
// codes. Every typed error and every domain outcome that crosses a boundary
// carries one, so a single identifier joins the Go value, the structured log,
// the HTTP/JSON body, the CLI message, and the docs. Codes are namespaced by
// subsystem (NAMESPACE-### ); the string is a stable contract — never reword an
// existing code, add a new one.
package codes

import "fmt"

// Code is one distinct problem condition.
type Code string

// Coded is implemented by errors and outcome values that carry a Code.
type Coded interface {
	Code() Code
}

// Gate-check outcomes (apply-time, fail-closed pre-check).
const (
	GateNotClassified Code = "GATE-001" // PR never finalized a plan; apply fails closed
	GateNotSatisfied  Code = "GATE-002" // a required grant is not yet ACTIVE
	GateUnconfirmable Code = "GATE-003" // gate state could not be freshly confirmed (backend unreachable)
	GateUnreachable   Code = "GATE-004" // the approval server itself is unreachable (transport failure)
)

// Wire / protocol decode.
const (
	UnknownStatus Code = "WIRE-001" // a decoded payload carried a status outside the known set
)

// Server-internal.
const (
	Internal Code = "SRV-001" // unexpected server-side error
)

// All returns every registered code (for the uniqueness/format test and the
// generated docs table).
func All() []Code {
	return []Code{
		GateNotClassified, GateNotSatisfied, GateUnconfirmable, GateUnreachable,
		UnknownStatus,
		Internal,
	}
}

// Error is a coded operational error: it pairs a stable Code with a message so
// callers can branch on errors.As(err, *codes.Error) / Code() instead of
// matching message text.
type Error struct {
	C   Code
	Msg string
}

func (e *Error) Error() string { return string(e.C) + ": " + e.Msg }

// Code implements Coded.
func (e *Error) Code() Code { return e.C }

// Errorf builds a coded *Error with a formatted message.
func Errorf(c Code, format string, a ...any) error {
	return &Error{C: c, Msg: fmt.Sprintf(format, a...)}
}
