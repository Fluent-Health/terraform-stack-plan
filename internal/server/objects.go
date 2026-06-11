package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ObjectStore persists completed log objects (and, later, plan/verify outputs).
// FSStore backs tests/local; a cloud impl (e.g. GCS) is wired by the serve
// command for deployment.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// objectKey is the canonical store key for a stack's log object. The exec/stack
// components are sanitized so the key is a stable, traversal-free path.
func objectKey(exec, stack string) string {
	return fmt.Sprintf("executions/%s/%s/log", sanitizeComponent(exec), sanitizeComponent(stack))
}

// FSStore is a filesystem-backed ObjectStore rooted at Root.
type FSStore struct{ Root string }

var _ ObjectStore = FSStore{}

// resolve maps a key to a path contained within Root (defense in depth).
func (s FSStore) resolve(key string) (string, bool) {
	if s.Root == "" {
		return "", false
	}
	p := filepath.Join(s.Root, filepath.FromSlash(key))
	cleanRoot := filepath.Clean(s.Root) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(p)+string(os.PathSeparator), cleanRoot) {
		return "", false
	}
	return p, true
}

func (s FSStore) Put(_ context.Context, key string, r io.Reader) error {
	p, ok := s.resolve(key)
	if !ok {
		return fmt.Errorf("fsstore: unsafe key %q", key)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (s FSStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, ok := s.resolve(key)
	if !ok {
		return nil, fmt.Errorf("fsstore: unsafe key %q", key)
	}
	return os.Open(p)
}
