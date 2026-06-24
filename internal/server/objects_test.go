package server

import (
	"strings"
	"testing"
)

func TestObjectKey(t *testing.T) {
	k := objectKey("e1", "stacks/a")
	if !strings.HasPrefix(k, "executions/e1/") || !strings.HasSuffix(k, "/log") {
		t.Fatalf("objectKey = %q", k)
	}
	if strings.Contains(k, "stacks/a") {
		t.Errorf("stack slashes should be sanitized in the key: %q", k)
	}
}
