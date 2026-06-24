package server

import "fmt"

// execStreamID is the event-stream id for a (pr, environment) gate lifecycle.
func execStreamID(pr int, env string) string { return fmt.Sprintf("exec:%d:%s", pr, env) }
