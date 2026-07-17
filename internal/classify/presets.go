package classify

import (
	"regexp"
)

// iamPattern matches IAM resources across the major providers. `actions` is
// left unset so an in-place policy update classifies as iam, not just creates.
var iamPattern = regexp.MustCompile(
	`(?:_iam_(?:policy|binding|member|audit_config)$)` + // google / google-beta
		`|^aws_iam_` +
		`|^azurerm_role_(?:assignment|definition)$`,
)

// PresetNames lists available built-in preset names (for error messages).
var PresetNames = []string{"iam"}

// PresetRule returns the built-in rule bundle for name. iconOverride replaces
// the preset's default glyph when non-empty; emitAttributes is carried onto
// the rule. ok is false for unknown names.
func PresetRule(name, iconOverride string, emitAttributes []string) (Rule, bool) {
	switch name {
	case "iam":
		r := Rule{Name: "iam", Icon: "🔐", TypePattern: iamPattern, MinCount: 1, EmitAttributes: emitAttributes}
		if iconOverride != "" {
			r.Icon = iconOverride
		}
		return r, true
	default:
		return Rule{}, false
	}
}
