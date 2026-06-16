package statemove

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const shimPrefix = "_tfsp_move."
const shimSuffix = ".tf"

// ShimFileName is the colocated shim filename for a key, e.g. "_tfsp_move.PR-123.tf".
func ShimFileName(key string) string { return shimPrefix + key + shimSuffix }

// keyFromFile extracts the key from a shim filename, or "" if it isn't a shim.
func keyFromFile(name string) string {
	if strings.HasPrefix(name, shimPrefix) && strings.HasSuffix(name, shimSuffix) {
		return name[len(shimPrefix) : len(name)-len(shimSuffix)]
	}
	return ""
}

// Shim is a discovered shim file.
type Shim struct {
	Path  string // path to the .tf
	Stack string // dir containing it, relative to the discover root (slash form)
	Key   string
	Ops   []Op
}

// walkManifestFiles walks root and invokes fn(path, key) for every file in our
// reserved namespace, where key is the filename-authoritative manifest key
// (keyOf returns "" for a file that is not ours). keyOf is keyFromFile for shims
// and xmoveKeyFromFile for xmove manifests. Errors from fn (and the walk) abort.
func walkManifestFiles(root string, keyOf func(name string) string, fn func(path, key string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		key := keyOf(d.Name())
		if key == "" {
			return nil
		}
		return fn(path, key)
	})
}

// Discover walks root for `_tfsp_move.*.tf` shim files and parses each. It is
// fail-closed: a file in our reserved namespace that won't parse, or whose
// header key disagrees with its filename, returns an error (the filename is the
// authoritative key). Use Cleanup to remove such a file.
func Discover(root string) ([]Shim, error) {
	var out []Shim
	err := walkManifestFiles(root, keyFromFile, func(path, fileKey string) error {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		pk, ops, perr := ParseShim(string(data))
		if perr != nil {
			return fmt.Errorf("state-move shim %s: %w", path, perr)
		}
		if pk != fileKey {
			return fmt.Errorf("state-move shim %s: key mismatch (filename %q != header %q)", path, fileKey, pk)
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		out = append(out, Shim{Path: path, Stack: filepath.ToSlash(rel), Key: fileKey, Ops: ops})
		return nil
	})
	return out, err
}

// Cleanup removes same-state shim files (_tfsp_move.*.tf) matching key;
// an empty key removes ALL shims. Returns the number of files removed.
func Cleanup(root, key string) (int, error) {
	shims, err := Discover(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range shims {
		if key != "" && s.Key != key {
			continue
		}
		if err := os.Remove(s.Path); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// CleanupXMoves removes cross-state xmove manifests (_tfsp_xmove.*.hcl) matching
// key; an empty key removes ALL xmove manifests. Returns the number removed.
func CleanupXMoves(root, key string) (int, error) {
	found, err := DiscoverXMoves(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range found {
		if key != "" && f.Key != key {
			continue
		}
		if err := os.Remove(f.Path); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
