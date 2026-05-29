// Package classify applies an ordered list of rules to a parsed stack and
// returns its class. It is pure: it does not know whether a rule originated
// from a preset or a custom config block.
package classify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/plan"
)

// Rule is a single classification rule with a deliberately small matcher.
type Rule struct {
	Name        string
	Icon        string
	TypePattern *regexp.Regexp // nil → match any resource type
	Actions     []string       // nil/empty → match any action; else all listed must be present
	MinCount    int            // minimum matching changes for the rule to fire (treated as 1 if <1)
	EmitAttributes []string   // attribute names to extract from matched changes
}

// Result is the outcome of classifying a stack: the chosen class, plus — for
// the firing rule's EmitAttributes — the sorted-unique non-null values found
// across the changes that rule matched. Attributes is nil when nothing emits.
type Result struct {
	Class      model.Class
	Attributes map[string][]string
}

// Classify returns the Result for the first rule that matches enough changes,
// or def (with no attributes) when none match.
func Classify(s plan.RawStack, rules []Rule, def model.Class) Result {
	for _, r := range rules {
		min := r.MinCount
		if min < 1 {
			min = 1
		}
		var matched []plan.RawChange
		for _, c := range s.Changes {
			if ruleMatchesChange(r, c) {
				matched = append(matched, c)
			}
		}
		if len(matched) >= min {
			return Result{
				Class:      model.Class{Name: r.Name, Icon: r.Icon},
				Attributes: extract(matched, r.EmitAttributes),
			}
		}
	}
	return Result{Class: def}
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

// scalarString stringifies a JSON scalar for the sidecar.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
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
