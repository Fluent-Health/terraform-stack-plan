// Package presets provides built-in named classification rule bundles that a
// repo can opt into without hand-writing regexes.
package presets

import (
	"regexp"

	"github.com/Fluent-Health/terraform-stack-plan/internal/classify"
)

// iamPattern matches IAM resources across the major providers. `actions` is
// left unset so an in-place policy update classifies as iam, not just creates.
var iamPattern = regexp.MustCompile(
	`(?:_iam_(?:policy|binding|member|audit_config)$)` + // google / google-beta
		`|^aws_iam_` +
		`|^azurerm_role_(?:assignment|definition)$`,
)

// Names lists available preset names (for error messages).
var Names = []string{"iam"}

// Get returns the rule bundle for name. iconOverride replaces the preset's
// default glyph when non-empty; emitAttributes is carried onto the rule. ok is
// false for unknown names.
func Get(name, iconOverride string, emitAttributes []string) (classify.Rule, bool) {
	switch name {
	case "iam":
		r := classify.Rule{Name: "iam", Icon: "🔐", TypePattern: iamPattern, MinCount: 1, EmitAttributes: emitAttributes}
		if iconOverride != "" {
			r.Icon = iconOverride
		}
		return r, true
	default:
		return classify.Rule{}, false
	}
}
