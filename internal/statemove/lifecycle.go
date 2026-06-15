package statemove

import (
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

// Discover walks root for `_tfsp_move.*.tf` shim files and parses each.
func Discover(root string) ([]Shim, error) {
	var out []Shim
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if keyFromFile(d.Name()) == "" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		pk, ops, perr := ParseShim(string(data))
		if perr != nil {
			return nil // a file matching the glob but not our format — skip
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		out = append(out, Shim{Path: path, Stack: filepath.ToSlash(rel), Key: pk, Ops: ops})
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
