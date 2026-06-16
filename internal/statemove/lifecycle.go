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

// Cleanup removes same-state shim files (_tfsp_move.*.tf) matching key; an empty
// key removes ALL shims. It matches by FILENAME and does not parse, so a corrupt
// or key-mismatched shim (which Discover rejects) is still removable. Returns the
// number of files removed.
func Cleanup(root, key string) (int, error) {
	var paths []string
	err := walkManifestFiles(root, keyFromFile, func(path, fileKey string) error {
		if key == "" || fileKey == key {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for i, p := range paths {
		if rerr := os.Remove(p); rerr != nil {
			return i, rerr
		}
	}
	return len(paths), nil
}

// CleanupXMoves removes cross-state xmove manifests (_tfsp_xmove.*.hcl) matching
// key; an empty key removes ALL. Like Cleanup it matches by filename and does not
// parse. Returns the number removed.
func CleanupXMoves(root, key string) (int, error) {
	var paths []string
	err := walkManifestFiles(root, xmoveKeyFromFile, func(path, fileKey string) error {
		if key == "" || fileKey == key {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for i, p := range paths {
		if rerr := os.Remove(p); rerr != nil {
			return i, rerr
		}
	}
	return len(paths), nil
}
