package runner

import (
	"regexp"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

var whyRegex = regexp.MustCompile(`^([^\s]+)\s+-\s+stack changed because "(.*)" changed because (.*)$`)

func ParseLine(line string) events.ChangeReason {
	m := whyRegex.FindStringSubmatch(line)
	if m != nil {
		stack := m[1]
		trigger := m[2]
		reason := m[3]

		kind := "direct"
		if strings.Contains(reason, "module") {
			kind = "module"
		} else if strings.Contains(reason, "watch") || strings.Contains(trigger, "components") {
			kind = "watch"
		}

		return events.ChangeReason{
			Stack: stack,
			Kind:  kind,
			Via:   []string{trigger},
		}
	}

	// Fallback for direct stack changes or other formats:
	// e.g. "stacks/b - stack has unmerged changes"
	parts := strings.SplitN(line, " - ", 2)
	if len(parts) == 2 {
		stack := strings.TrimSpace(parts[0])
		reason := strings.TrimSpace(parts[1])
		if reason == "stack has unmerged changes" {
			return events.ChangeReason{
				Stack: stack,
				Kind:  "direct",
				Via:   []string{stack},
			}
		}
		kind := "direct"
		if strings.Contains(reason, "module") {
			kind = "module"
		} else if strings.Contains(reason, "watch") {
			kind = "watch"
		}
		return events.ChangeReason{
			Stack: stack,
			Kind:  kind,
			Via:   []string{stack},
		}
	}

	return events.ChangeReason{Stack: line, Kind: "unknown"}
}
