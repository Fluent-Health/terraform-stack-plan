// Package domain holds the canonical value types shared across the render
// pipeline, the runner→server wire protocol, and persistence. Each concept is
// defined exactly once here; internal/model, internal/events, and
// internal/classify re-export these via type aliases so there is a single
// source of truth (no parallel re-modelling, no lossy conversion).
//
// It is a leaf package: it imports nothing from this module, so any package may
// depend on it without risking an import cycle.
package domain

// Counts is a stack's per-kind operation tally (pure no-ops excluded).
// Move/Import/Forget are tracked separately from the create/change/destroy/
// replace buckets. All fields omitempty so a zero stack marshals compactly.
type Counts struct {
	Add             int `json:"add,omitempty"`
	Change          int `json:"change,omitempty"`
	Destroy         int `json:"destroy,omitempty"`
	Replace         int `json:"replace,omitempty"`
	Move            int `json:"move,omitempty"`
	Import          int `json:"import,omitempty"`
	Forget          int `json:"forget,omitempty"`
	SensitivityOnly int `json:"sensitivity_only,omitempty"`
}

// Total returns the sum of the create/change/destroy/replace buckets.
func (c Counts) Total() int { return c.Add + c.Change + c.Destroy + c.Replace }

// AnyChange reports whether the stack has anything worth rendering — a config
// change, or a move / import / forget.
func (c Counts) AnyChange() bool { return c.Total()+c.Move+c.Import+c.Forget > 0 }

// Category is one matched classification rule's outcome: its name, optional
// glyph, and — for the rule's emitted attributes — the sorted-unique non-null
// values across the changes it matched. Attributes is nil when nothing was
// emitted.
type Category struct {
	Name       string              `json:"name"`
	Icon       string              `json:"icon,omitempty"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// Label renders the category as "icon name" or just "name".
func (c Category) Label() string {
	if c.Icon == "" {
		return c.Name
	}
	return c.Icon + " " + c.Name
}
