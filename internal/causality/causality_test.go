package causality

import (
	"testing"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
)

func TestParseWhyChanged(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected events.ChangeReason
	}{
		{
			name:  "module changed",
			input: `stacks/nonprod/am/fh-dev-svc - stack changed because "../../../../components/am/am" changed because module "../../../../components/am/am" has unmerged changes`,
			expected: events.ChangeReason{
				Stack: "stacks/nonprod/am/fh-dev-svc",
				Kind:  "module",
				Via:   []string{"../../../../components/am/am"},
			},
		},
		{
			name:  "watch changed",
			input: `stacks/nonprod/db/postgres - stack changed because "../../../../components/db/postgres" changed because watch file "../../../../components/db/postgres/main.tf" changed`,
			expected: events.ChangeReason{
				Stack: "stacks/nonprod/db/postgres",
				Kind:  "watch",
				Via:   []string{"../../../../components/db/postgres"},
			},
		},
		{
			name:  "direct changed",
			input: `stacks/nonprod/networking - stack changed because "stacks/nonprod/networking" changed because file "stacks/nonprod/networking/main.tf" changed`,
			expected: events.ChangeReason{
				Stack: "stacks/nonprod/networking",
				Kind:  "direct",
				Via:   []string{"stacks/nonprod/networking"},
			},
		},
		{
			name:  "unknown line",
			input: `stacks/nonprod/unknown`,
			expected: events.ChangeReason{
				Stack: "stacks/nonprod/unknown",
				Kind:  "unknown",
				Via:   nil,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ParseLine(tc.input)
			if res.Stack != tc.expected.Stack {
				t.Fatalf("expected stack %q, got %q", tc.expected.Stack, res.Stack)
			}
			if res.Kind != tc.expected.Kind {
				t.Fatalf("expected kind %q, got %q", tc.expected.Kind, res.Kind)
			}
			if len(res.Via) != len(tc.expected.Via) {
				t.Fatalf("expected via length %d, got %d", len(tc.expected.Via), len(res.Via))
			}
			for i, v := range res.Via {
				if v != tc.expected.Via[i] {
					t.Fatalf("expected via[%d] %q, got %q", i, tc.expected.Via[i], v)
				}
			}
		})
	}
}
