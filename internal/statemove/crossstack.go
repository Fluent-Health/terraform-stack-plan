package statemove

import (
	"fmt"

	tfjson "github.com/hashicorp/terraform-json"
)

// importID returns the destroyed resource's prior id (its Terraform import id),
// or an error when absent/non-string.
func importID(rc *tfjson.ResourceChange) (string, error) {
	before, _ := rc.Change.Before.(map[string]any)
	id, ok := before["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("%q has no string before.id to import with", rc.Address)
	}
	return id, nil
}

// CrossStackPairs validates a cross-stack move and returns the matched from→to
// address pairs (fail-closed; same rules as ClassifyCrossStack minus the import
// id). Used by the `state mv` path, which is address-based and needs no id.
func CrossStackPairs(src, dst *tfjson.Plan, from, to string) ([]Move, error) {
	if from == "" || to == "" {
		return nil, fmt.Errorf("cross-stack move: empty address (from=%q to=%q)", from, to)
	}
	deletes := map[string]*tfjson.ResourceChange{}
	for _, rc := range src.ResourceChanges {
		if rc.Change != nil && rc.Change.Actions.Delete() && matches(rc.Address, from) {
			deletes[rc.Address] = rc
		}
	}
	creates := map[string]*tfjson.ResourceChange{}
	for _, rc := range dst.ResourceChanges {
		if rc.Change != nil && rc.Change.Actions.Create() && matches(rc.Address, to) {
			creates[rc.Address] = rc
		}
	}
	if len(deletes) == 0 {
		return nil, fmt.Errorf("cross-stack move %s → %s: nothing under %q is destroyed in the source plan", from, to, from)
	}
	var pairs []Move
	matched := map[string]bool{}
	for _, addr := range sortedKeys(deletes) {
		d := deletes[addr]
		want := to + d.Address[len(from):]
		c, ok := creates[want]
		if !ok {
			return nil, fmt.Errorf("cross-stack move %s → %s: %q is destroyed but %q is not created at the destination", from, to, d.Address, want)
		}
		if c.Type != d.Type {
			return nil, fmt.Errorf("cross-stack move %s → %s: type mismatch %q (%s) vs %q (%s)", from, to, d.Address, d.Type, c.Address, c.Type)
		}
		pairs = append(pairs, Move{From: d.Address, To: want})
		matched[want] = true
	}
	for _, caddr := range sortedKeys(creates) {
		if !matched[caddr] {
			return nil, fmt.Errorf("cross-stack move %s → %s: %q is created at the destination but has no destroyed counterpart under %q", from, to, caddr, from)
		}
	}
	return pairs, nil
}

// CheckXMoveSource verifies that src has a prior_state containing at least one
// resource under from. Returns an error when prior_state is absent (source stack
// has no state — nothing to move) or when nothing under from is found (wrong
// address prefix or wrong stack).
func CheckXMoveSource(src *tfjson.Plan, from string) error {
	if src.PriorState == nil || src.PriorState.Values == nil {
		return fmt.Errorf("source stack has no prior state — nothing to move")
	}
	addrs := stateAddresses(src.PriorState)
	for a := range addrs {
		if matches(a, from) {
			return nil
		}
	}
	return fmt.Errorf("nothing under %q in source prior state (wrong from address or stack?)", from)
}

// ClassifyCrossStack validates a cross-stack move from `from` (in src's plan) to
// `to` (in dst's plan) and returns the ops to write: `removed` ops in the source
// shim and `import` ops (id from src's before.id) in the destination shim. Fail-
// closed: every resource destroyed under `from` must be created under `to` at the
// rewritten address with the same type and a usable import id, and every create
// under `to` must have a destroyed counterpart.
func ClassifyCrossStack(src, dst *tfjson.Plan, from, to string) (srcOps, dstOps []Op, err error) {
	pairs, err := CrossStackPairs(src, dst, from, to)
	if err != nil {
		return nil, nil, err
	}
	delByAddr := map[string]*tfjson.ResourceChange{}
	for _, rc := range src.ResourceChanges {
		if rc.Change != nil && rc.Change.Actions.Delete() {
			delByAddr[rc.Address] = rc
		}
	}
	for _, p := range pairs {
		id, ierr := importID(delByAddr[p.From])
		if ierr != nil {
			return nil, nil, fmt.Errorf("cross-stack move %s → %s: %w", from, to, ierr)
		}
		srcOps = append(srcOps, Op{Kind: "removed", From: p.From})
		dstOps = append(dstOps, Op{Kind: "import", To: p.To, ID: id})
	}
	return srcOps, dstOps, nil
}
