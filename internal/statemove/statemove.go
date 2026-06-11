// Package statemove validates declared Terraform resource moves against plan
// data and generates/maintains the native `moved {}` shim files that effect them
// (SP1: within one stack). It consumes tfplan.json — it does not run terraform.
package statemove

import (
	"fmt"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// Move is one declared relocation, from one address to another (same stack in
// SP1). Addresses are opaque Terraform addresses (resource, module, or with
// count/for_each brackets).
type Move struct {
	From string
	To   string
}

// matches reports whether addr is target itself or a child of target (a module's
// resources, or a resource's instances). The "."/"[" boundary keeps "module.x"
// from matching a sibling "module.x_other".
func matches(addr, target string) bool {
	return addr == target ||
		strings.HasPrefix(addr, target+".") ||
		strings.HasPrefix(addr, target+"[")
}

// ValidateMove fail-closes unless the plan shows a clean relocation from→to:
// every resource being destroyed under `from` has a created counterpart under
// `to` at the rewritten address with the same type, and vice versa (no unmatched
// creates under `to`). Works for a single resource (exact match), a whole module
// (prefix), and count/for_each (bracketed) forms.
func ValidateMove(plan *tfjson.Plan, from, to string) error {
	if from == "" || to == "" {
		return fmt.Errorf("move: empty address (from=%q to=%q)", from, to)
	}
	if from == to {
		return fmt.Errorf("move: from and to are identical (%q)", from)
	}
	deletes := map[string]*tfjson.ResourceChange{}
	creates := map[string]*tfjson.ResourceChange{}
	for _, rc := range plan.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		switch {
		case rc.Change.Actions.Delete() && matches(rc.Address, from):
			deletes[rc.Address] = rc
		case rc.Change.Actions.Create() && matches(rc.Address, to):
			creates[rc.Address] = rc
		}
	}
	if len(deletes) == 0 {
		return fmt.Errorf("move %s → %s: nothing under %q is being destroyed in the plan", from, to, from)
	}
	matched := map[string]bool{}
	for _, addr := range sortedKeys(deletes) {
		d := deletes[addr]
		want := to + d.Address[len(from):]
		c, ok := creates[want]
		if !ok {
			return fmt.Errorf("move %s → %s: %q is destroyed but %q is not created (plan not in order)", from, to, d.Address, want)
		}
		if c.Type != d.Type {
			return fmt.Errorf("move %s → %s: type mismatch %q (%s) vs %q (%s)", from, to, d.Address, d.Type, c.Address, c.Type)
		}
		matched[want] = true
	}
	for _, caddr := range sortedKeys(creates) {
		if !matched[caddr] {
			return fmt.Errorf("move %s → %s: %q is created but has no destroyed counterpart under %q", from, to, caddr, from)
		}
	}
	return nil
}

func sortedKeys(m map[string]*tfjson.ResourceChange) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
