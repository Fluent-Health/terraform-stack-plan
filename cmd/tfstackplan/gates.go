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

// countsFromSidecar extracts each stack's operation counts from the
// classification sidecar, for the server's blast-radius bar and op summaries.
func countsFromSidecar(data []byte) (map[string]events.Counts, error) {
	var doc sidecarDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]events.Counts{}
	for path, entry := range doc.Stacks {
		out[path] = entry.Counts
	}
	return out, nil
}

// categoriesFromSidecar extracts each stack's matched categories (name + icon)
// from the classification sidecar, for the server's group-DAG badges.
func categoriesFromSidecar(data []byte) (map[string][]events.Category, error) {
	var doc sidecarDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string][]events.Category{}
	for path, entry := range doc.Stacks {
		var cats []events.Category
		for _, c := range entry.Categories {
			icon := ""
			if c.Icon != nil {
				icon = *c.Icon
			}
			cats = append(cats, events.Category{Name: c.Category, Icon: icon})
		}
		if len(cats) > 0 {
			out[path] = cats
		}
	}
	return out, nil
}
