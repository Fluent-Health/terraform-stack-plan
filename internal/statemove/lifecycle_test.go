package statemove

import (
	"os"
	"path/filepath"
	"testing"
)

func writeXMoveManifest(t *testing.T, dir, key string, xm XMove) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, XMoveFileName(key)), []byte(RenderXMove(key, xm)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeShim(t *testing.T, dir, key string, ops []Op) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ShimFileName(key)), []byte(RenderShim(key, ops)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShimFileName(t *testing.T) {
	if got := ShimFileName("PR-123"); got != "_tfsp_move.PR-123.tf" {
		t.Errorf("ShimFileName = %q", got)
	}
}

func TestDiscoverAndCleanup(t *testing.T) {
	root := t.TempDir()
	writeShim(t, filepath.Join(root, "stacks/a"), "PR-1", []Op{{Kind: "moved", From: "x.a", To: "x.b"}})
	writeShim(t, filepath.Join(root, "stacks/a"), "PR-2", []Op{{Kind: "moved", From: "y.a", To: "y.b"}})
	writeShim(t, filepath.Join(root, "stacks/b"), "PR-1", []Op{{Kind: "moved", From: "z.a", To: "z.b"}})
	_ = os.WriteFile(filepath.Join(root, "stacks/a", "main.tf"), []byte("resource \"x\" \"y\" {}"), 0o644)

	all, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("Discover = %d shims, want 3", len(all))
	}

	n, err := Cleanup(root, "PR-1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Cleanup(PR-1) removed %d, want 2", n)
	}
	left, _ := Discover(root)
	if len(left) != 1 || left[0].Key != "PR-2" {
		t.Errorf("after cleanup, remaining = %+v, want one PR-2", left)
	}

	n, _ = Cleanup(root, "")
	if n != 1 {
		t.Errorf("Cleanup(all) removed %d, want 1", n)
	}
	if rest, _ := Discover(root); len(rest) != 0 {
		t.Errorf("after cleanup-all, %d remain", len(rest))
	}
}

func TestDiscoverAndCleanupXMoves(t *testing.T) {
	root := t.TempDir()
	xm := XMove{SourceStack: "src", Pairs: []Move{{From: "a.b", To: "a.b"}}}
	writeXMoveManifest(t, filepath.Join(root, "stacks/dst1"), "PR-5", xm)
	writeXMoveManifest(t, filepath.Join(root, "stacks/dst2"), "PR-5", xm)
	writeXMoveManifest(t, filepath.Join(root, "stacks/dst3"), "PR-6", xm)

	all, err := DiscoverXMoves(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("DiscoverXMoves = %d, want 3", len(all))
	}

	n, err := CleanupXMoves(root, "PR-5")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("CleanupXMoves(PR-5) removed %d, want 2", n)
	}
	left, _ := DiscoverXMoves(root)
	if len(left) != 1 || left[0].Key != "PR-6" {
		t.Errorf("after cleanup, remaining = %+v, want one PR-6", left)
	}

	n, _ = CleanupXMoves(root, "")
	if n != 1 {
		t.Errorf("CleanupXMoves(all) removed %d, want 1", n)
	}
	if rest, _ := DiscoverXMoves(root); len(rest) != 0 {
		t.Errorf("after cleanup-all, %d remain", len(rest))
	}
}
