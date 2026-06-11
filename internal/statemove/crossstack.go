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

// ClassifyCrossStack validates a cross-stack move from `from` (in src's plan) to
// `to` (in dst's plan) and returns the ops to write: `removed` ops in the source
// shim and `import` ops (id from src's before.id) in the destination shim. Fail-
// closed: every resource destroyed under `from` must be created under `to` at the
// rewritten address with the same type and a usable import id, and every create
// under `to` must have a destroyed counterpart.
func ClassifyCrossStack(src, dst *tfjson.Plan, from, to string) (srcOps, dstOps []Op, err error) {
	if from == "" || to == "" {
		return nil, nil, fmt.Errorf("cross-stack move: empty address (from=%q to=%q)", from, to)
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
		return nil, nil, fmt.Errorf("cross-stack move %s → %s: nothing under %q is destroyed in the source plan", from, to, from)
	}
	matched := map[string]bool{}
	for _, addr := range sortedKeys(deletes) {
		d := deletes[addr]
		want := to + d.Address[len(from):]
		c, ok := creates[want]
		if !ok {
			return nil, nil, fmt.Errorf("cross-stack move %s → %s: %q is destroyed but %q is not created at the destination", from, to, d.Address, want)
		}
		if c.Type != d.Type {
			return nil, nil, fmt.Errorf("cross-stack move %s → %s: type mismatch %q (%s) vs %q (%s)", from, to, d.Address, d.Type, c.Address, c.Type)
		}
		id, ierr := importID(d)
		if ierr != nil {
			return nil, nil, fmt.Errorf("cross-stack move %s → %s: %w", from, to, ierr)
		}
		srcOps = append(srcOps, Op{Kind: "removed", From: d.Address})
		dstOps = append(dstOps, Op{Kind: "import", To: want, ID: id})
		matched[want] = true
	}
	for _, caddr := range sortedKeys(creates) {
		if !matched[caddr] {
			return nil, nil, fmt.Errorf("cross-stack move %s → %s: %q is created at the destination but has no destroyed counterpart under %q", from, to, caddr, from)
		}
	}
	return srcOps, dstOps, nil
}
