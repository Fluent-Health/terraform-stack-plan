package server

import "fmt"

// execStreamID is the event-stream id for a (pr, environment) gate lifecycle.
func execStreamID(pr int, env string) string { return fmt.Sprintf("exec:%d:%s", pr, env) }

// runStreamID is the event-stream id for one execution's lifecycle aggregate
// (internal/execution). Distinct from execStreamID (the (pr,env) gate stream).
func runStreamID(execID string) string { return "run:" + execID }
