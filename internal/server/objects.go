package server

import (
	"context"
	"fmt"
	"io"
)

// ObjectStore persists completed log objects (and, later, plan/verify outputs).
// A cloud impl (e.g. GCS) is wired by the serve command for deployment.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// objectKey is the canonical store key for a stack's log object. The exec/stack
// components are sanitized so the key is a stable, traversal-free path.
func objectKey(exec, stack string) string {
	return fmt.Sprintf("executions/%s/%s/log", sanitizeComponent(exec), sanitizeComponent(stack))
}
