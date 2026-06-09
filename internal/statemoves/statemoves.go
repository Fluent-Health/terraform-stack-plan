// Package statemoves loads the optional --state-moves manifest: per stack, the
// addresses being moved IN by an external cross-state move (infra's state-mover).
// A planned create of such an address is a relocation, not a real create, so
// classification treats it as non-mutating.
package statemoves

import (
	"encoding/json"
	"fmt"
	"os"
)

type Set map[string]bool

func (s Set) Len() int { return len(s) }

type Manifest struct{ byStack map[string]Set }

// Targets returns the move-target set for a stack (empty, never nil, if none).
func (m Manifest) Targets(stack string) Set {
	if s := m.byStack[stack]; s != nil {
		return s
	}
	return Set{}
}

// Load reads the JSON manifest; an empty path returns an empty manifest (fail-safe).
func Load(path string) (Manifest, error) {
	if path == "" {
		return Manifest{byStack: map[string]Set{}}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read state-moves %q: %w", path, err)
	}
	var raw map[string][]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return Manifest{}, fmt.Errorf("parse state-moves %q: %w", path, err)
	}
	byStack := make(map[string]Set, len(raw))
	for stack, addrs := range raw {
		set := make(Set, len(addrs))
		for _, a := range addrs {
			set[a] = true
		}
		byStack[stack] = set
	}
	return Manifest{byStack: byStack}, nil
}
