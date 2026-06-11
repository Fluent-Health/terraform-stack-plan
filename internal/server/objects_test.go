package server

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestFSStorePutGet(t *testing.T) {
	s := FSStore{Root: t.TempDir()}
	if err := s.Put(context.Background(), "executions/e1/stacks__a/log", strings.NewReader("hello world")); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(context.Background(), "executions/e1/stacks__a/log")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "hello world" {
		t.Fatalf("got %q", b)
	}
	if _, err := s.Get(context.Background(), "nope"); err == nil {
		t.Error("Get of a missing key should error")
	}
}

func TestFSStoreRejectsTraversal(t *testing.T) {
	s := FSStore{Root: t.TempDir()}
	if err := s.Put(context.Background(), "../../etc/evil", strings.NewReader("x")); err == nil {
		t.Error("Put with a traversing key should error")
	}
	if _, err := s.Get(context.Background(), "../../etc/passwd"); err == nil {
		t.Error("Get with a traversing key should error")
	}
}

func TestObjectKey(t *testing.T) {
	k := objectKey("e1", "stacks/a")
	if !strings.HasPrefix(k, "executions/e1/") || !strings.HasSuffix(k, "/log") {
		t.Fatalf("objectKey = %q", k)
	}
	if strings.Contains(k, "stacks/a") {
		t.Errorf("stack slashes should be sanitized in the key: %q", k)
	}
}
