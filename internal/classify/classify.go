// Package classify applies an ordered list of rules to a parsed stack and
// returns its class. It is pure: it does not know whether a rule originated
// from a preset or a custom config block.
package classify

import (
	"regexp"

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
}

// Classify returns the class of the first rule that matches enough changes,
// or def when none match.
func Classify(s plan.RawStack, rules []Rule, def model.Class) model.Class {
	for _, r := range rules {
		min := r.MinCount
		if min < 1 {
			min = 1
		}
		matched := 0
		for _, c := range s.Changes {
			if ruleMatchesChange(r, c) {
				matched++
			}
		}
		if matched >= min {
			return model.Class{Name: r.Name, Icon: r.Icon}
		}
	}
	return def
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
