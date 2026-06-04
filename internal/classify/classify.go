// Package classify applies an ordered list of rules to a parsed stack and
// returns the set of matching categories. It is pure: it does not know whether
// a rule originated from a preset or a custom config block.
package classify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
)

// Rule is a single classification rule with a deliberately small matcher.
type Rule struct {
	Name           string
	Icon           string
	TypePattern    *regexp.Regexp // nil → match any resource type
	Actions        []string       // nil/empty → match any action; else all listed must be present
	MinCount       int            // minimum matching changes for the rule to fire (treated as 1 if <1)
	EmitAttributes []string       // attribute names to extract from matched changes
}

// Category is one matched rule's outcome: its name, icon, and — for the rule's
// EmitAttributes — the sorted-unique non-null values across the changes it
// matched. Attributes is nil when nothing was emitted.
type Category struct {
	Name       string
	Icon       string
	Attributes map[string][]string
}

// Classify returns a Category for every rule that matches enough changes, in
// rule order. The slice is empty when no rule fires — the caller supplies the
// display fallback. Rules are independent; there is no first-match-wins.
func Classify(s plan.RawStack, rules []Rule) []Category {
	var cats []Category
	for _, r := range rules {
		min := r.MinCount
		if min < 1 {
			min = 1
		}
		var matched []plan.RawChange
		for _, c := range s.Changes {
			if !c.Action.Mutates() {
				continue // pure move/import/forget: no apply-time mutation to classify
			}
			if ruleMatchesChange(r, c) {
				matched = append(matched, c)
			}
		}
		if len(matched) >= min {
			attrs := extract(matched, r.EmitAttributes)
			// Stack-project fallback: bucket-scoped IAM (e.g.
			// google_storage_bucket_iam_member) exposes no project, so a rule that
			// emits "project" would surface none — and the per-project IAM gate
			// would request no PAM grant. Back-fill from the stack's unique project
			// (resolved from any sibling resource that does carry one). Only when
			// the emitted set is empty; never overrides a real value, never guesses
			// across multiple projects.
			if wantsProject(r.EmitAttributes) && len(attrs["project"]) == 0 {
				if sp := deriveStackProject(s.Changes); sp != "" {
					if attrs == nil {
						attrs = map[string][]string{}
					}
					attrs["project"] = []string{sp}
				}
			}
			cats = append(cats, Category{Name: r.Name, Icon: r.Icon, Attributes: attrs})
		}
	}
	return cats
}

// Summarize unions categories across stacks: for each rule (in rules order)
// that any stack matched, it returns one Category whose Attributes merge every
// matching stack's values (sorted-unique per key). Categories no stack matched
// are omitted.
func Summarize(perStack [][]Category, rules []Rule) []Category {
	agg := map[string]*Category{}
	for _, cats := range perStack {
		for _, c := range cats {
			a, ok := agg[c.Name]
			if !ok {
				a = &Category{Name: c.Name, Icon: c.Icon}
				agg[c.Name] = a
			}
			a.Attributes = mergeAttrs(a.Attributes, c.Attributes)
		}
	}
	var out []Category
	for _, r := range rules {
		if a, ok := agg[r.Name]; ok {
			out = append(out, *a)
		}
	}
	return out
}

// mergeAttrs returns the per-key sorted-unique union of two attribute maps,
// or nil when the result is empty.
func mergeAttrs(a, b map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, m := range []map[string][]string{a, b} {
		for k, vs := range m {
			seen := map[string]struct{}{}
			for _, x := range out[k] {
				seen[x] = struct{}{}
			}
			for _, v := range vs {
				if _, dup := seen[v]; !dup {
					seen[v] = struct{}{}
					out[k] = append(out[k], v)
				}
			}
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extract collects sorted-unique non-null scalar values of each requested
// attribute across the matched changes. Returns nil when names is empty or no
// values were found, so the sidecar omits the field.
func extract(matched []plan.RawChange, names []string) map[string][]string {
	if len(names) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, name := range names {
		seen := map[string]struct{}{}
		var vals []string
		for _, c := range matched {
			v, ok := c.Raw[name]
			if !ok || v == nil {
				continue
			}
			str := scalarString(v)
			if _, dup := seen[str]; dup {
				continue
			}
			seen[str] = struct{}{}
			vals = append(vals, str)
		}
		if len(vals) > 0 {
			sort.Strings(vals)
			out[name] = vals
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// wantsProject reports whether "project" is among the requested attributes.
func wantsProject(names []string) bool {
	for _, n := range names {
		if n == "project" {
			return true
		}
	}
	return false
}

// deriveStackProject returns the single project shared by the stack's changes,
// or "" when zero or more than one distinct project is present. Used to attribute
// projectless IAM resources (bucket/folder-scoped) to the project the rest of the
// stack manages. Conservative by design: ambiguity yields "" so the gate fails
// closed rather than guessing. Scans ALL changes (including non-mutating
// move/import/forget), unlike extract's mutating-only scope — any sibling
// identifies the stack's project regardless of its action.
func deriveStackProject(changes []plan.RawChange) string {
	seen := map[string]struct{}{}
	for _, c := range changes {
		v, ok := c.Raw["project"]
		if !ok || v == nil {
			continue
		}
		seen[scalarString(v)] = struct{}{}
	}
	if len(seen) == 1 {
		for p := range seen {
			return p
		}
	}
	return ""
}

// scalarString stringifies a JSON scalar for the sidecar.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func ruleMatchesChange(r Rule, c plan.RawChange) bool {
	if r.TypePattern != nil && !r.TypePattern.MatchString(c.Type) {
		return false
	}
	for _, want := range r.Actions {
		if !contains(c.Actions, want) {
			return false
		}
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
