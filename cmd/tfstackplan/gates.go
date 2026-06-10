package main

import (
	"encoding/json"
	"sort"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

// gatesFromSidecar derives the approval gates and moving stacks from the
// classification sidecar JSON. A gate is emitted for each (gating class, target)
// pair, where the targets are the deduped emitted-attribute values of that
// class's summary category (e.g. the iam category's "project" values). Moving
// stacks are those whose per-stack categories include the non-gating "move"
// category. gating is the set of class names that have a `class` binding in the
// config.
func gatesFromSidecar(data []byte, gating map[string]bool) (gates []events.GateTarget, moving []string, err error) {
	var doc sidecarDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	gates = []events.GateTarget{}
	for _, c := range doc.Summary.Categories {
		if !gating[c.Category] {
			continue
		}
		seen := map[string]bool{}
		var targets []string
		for _, vals := range c.Attributes {
			for _, v := range vals {
				if v != "" && !seen[v] {
					seen[v] = true
					targets = append(targets, v)
				}
			}
		}
		sort.Strings(targets)
		for _, tgt := range targets {
			gates = append(gates, events.GateTarget{Class: c.Category, Target: tgt})
		}
	}
	moving = []string{}
	for path, entry := range doc.Stacks {
		for _, c := range entry.Categories {
			if c.Category == moveCategory {
				moving = append(moving, path)
				break
			}
		}
	}
	sort.Strings(moving)
	return gates, moving, nil
}
