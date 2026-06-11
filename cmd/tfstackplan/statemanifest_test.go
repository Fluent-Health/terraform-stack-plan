package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/statemove"
)

// mustMkdir creates dir (and parents) or fatals.
func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeShim writes a rendered shim for key+ops into <root>/<stack>/.
func writeShim(t *testing.T, root, stack, key string, ops []statemove.Op) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(stack), statemove.ShimFileName(key))
	if err := os.WriteFile(path, []byte(statemove.RenderShim(key, ops)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeXMove writes a rendered xmove manifest for key+xm into <root>/<destStack>/.
func writeXMove(t *testing.T, root, destStack, key string, xm statemove.XMove) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(destStack), statemove.XMoveFileName(key))
	if err := os.WriteFile(path, []byte(statemove.RenderXMove(key, xm)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureStdout runs fn, capturing everything written to os.Stdout, and returns
// it as a string. fn receives the exit code (not checked here).
func captureStdout(t *testing.T, fn func() int) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf [1 << 20]byte
	n, _ := r.Read(buf[:])
	r.Close()
	return string(buf[:n])
}

func TestMovesManifestTwoSided(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "service-projects", "fh-dev-svc"))
	mustMkdir(t, filepath.Join(root, "cluster", "fh-dev-svc"))
	// source shim: a removed (move-out) op — the IAM destroy we must neutralize
	writeShim(t, root, "service-projects/fh-dev-svc", "PR-7", []statemove.Op{
		{Kind: "removed", From: "google_project_iam_member.node"},
	})
	// dest shim: the matching import (move-in)
	writeShim(t, root, "cluster/fh-dev-svc", "PR-7", []statemove.Op{
		{Kind: "import", To: "google_project_iam_member.node", ID: "proj roles/x sa"},
	})
	out := captureStdout(t, func() int {
		return runStateMovesManifest([]string{"--dir", root})
	})
	var got map[string][]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	wantSrc := []string{"google_project_iam_member.node"}
	if !reflect.DeepEqual(got["service-projects/fh-dev-svc"], wantSrc) {
		t.Errorf("source move-out missing: %v", got)
	}
	if !reflect.DeepEqual(got["cluster/fh-dev-svc"], wantSrc) {
		t.Errorf("dest move-in missing: %v", got)
	}
}

func TestMovesManifestXMoveBothStacks(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	mustMkdir(t, filepath.Join(root, "dst"))
	writeXMove(t, root, "dst", "PR-9", statemove.XMove{
		SourceStack: "src",
		Pairs:       []statemove.Move{{From: "module.a.google_x.y", To: "module.b.google_x.y"}},
	})
	out := captureStdout(t, func() int { return runStateMovesManifest([]string{"--dir", root}) })
	var got map[string][]string
	_ = json.Unmarshal([]byte(out), &got)
	if !reflect.DeepEqual(got["src"], []string{"module.a.google_x.y"}) {
		t.Errorf("xmove source addr missing: %v", got)
	}
	if !reflect.DeepEqual(got["dst"], []string{"module.b.google_x.y"}) {
		t.Errorf("xmove dest addr missing: %v", got)
	}
}

func TestMovesManifestPRFilter(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "s"))
	writeShim(t, root, "s", "PR-1", []statemove.Op{{Kind: "removed", From: "a.b"}})
	writeShim(t, root, "s", "PR-2", []statemove.Op{{Kind: "removed", From: "c.d"}})
	out := captureStdout(t, func() int { return runStateMovesManifest([]string{"--dir", root, "--pr", "1"}) })
	var got map[string][]string
	_ = json.Unmarshal([]byte(out), &got)
	if !reflect.DeepEqual(got["s"], []string{"a.b"}) {
		t.Errorf("--pr 1 should yield only a.b, got %v", got)
	}
}
