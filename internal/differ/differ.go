// Package differ turns a single attribute's before/after values into an ordered
// list of render variants (preferred → minimal). All value-type knowledge lives
// here; fit and render treat the result generically.
package differ

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"
)

type valueType int

const (
	typePlain valueType = iota
	typeJSON
	typeYAML
	typeBase64
)

var base64Re = regexp.MustCompile(`^[A-Za-z0-9+/\r\n]+={0,2}$`)

// detect sniffs the type of a string attribute value.
func detect(s string) valueType {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return typePlain
	}
	if json.Valid([]byte(trimmed)) && (trimmed[0] == '{' || trimmed[0] == '[') {
		return typeJSON
	}
	// base64: long, charset-only, decodes, and isn't obviously text/yaml.
	if len(trimmed) >= 100 && base64Re.MatchString(trimmed) {
		if _, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(trimmed, "\n", "")); err == nil {
			return typeBase64
		}
	}
	var y any
	if err := yaml.Unmarshal([]byte(s), &y); err == nil {
		switch y.(type) {
		case map[string]any, []any:
			return typeYAML
		}
	}
	return typePlain
}

func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case string:
		return fmt.Sprintf("%q", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// structuredKind picks the canonical format for a structured attribute: a
// JSON-detected string → "json", a YAML-detected string → "yaml", and a native
// map/list (no source text) → "yaml" (a clean, low-noise default).
func structuredKind(in Input) string {
	if s := firstNonEmpty(firstStr(in.Before), firstStr(in.After)); s != "" {
		switch detect(s) {
		case typeJSON:
			return "json"
		case typeYAML:
			return "yaml"
		}
	}
	return "yaml"
}

// canonical renders a parsed value as stable, sorted-key text so the diff is
// meaningful regardless of the provider's original formatting. A nil value
// (create's before / delete's after) renders as empty.
func canonical(v any, kind string) string {
	if v == nil {
		return ""
	}
	if kind == "json" {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(b) + "\n"
		}
	}
	if b, err := yaml.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v\n", v)
}

// contextDiff renders a unified diff with 2 lines of context. The ---/+++ file
// headers are dropped; each hunk's @@ header becomes a "⋮" separator (omitted
// before the first hunk). Context lines keep their leading space; changed lines
// keep -/+.
func contextDiff(before, after string) string {
	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(before), B: difflib.SplitLines(after), Context: 2,
	})
	if err != nil {
		return ""
	}
	var keep []string
	seenHunk := false
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "---") || strings.HasPrefix(ln, "+++"):
			continue
		case strings.HasPrefix(ln, "@@"):
			if seenHunk {
				keep = append(keep, "⋮")
			}
			seenHunk = true
		default:
			keep = append(keep, ln)
		}
	}
	return strings.Join(keep, "\n")
}

// lineDiff renders a unified line diff with 3 lines of context.
func lineDiff(before, after string) string {
	ud := difflib.UnifiedDiff{
		A:       difflib.SplitLines(before),
		B:       difflib.SplitLines(after),
		Context: 3,
	}
	out, err := difflib.GetUnifiedDiffString(ud)
	if err != nil {
		return summaryLine("value", "text", strings.Count(before, "\n"), 0)
	}
	var keep []string
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasPrefix(ln, "---") || strings.HasPrefix(ln, "+++") || strings.HasPrefix(ln, "@@") {
			continue
		}
		keep = append(keep, ln)
	}
	return strings.Join(keep, "\n")
}

// summaryLine renders the magnitude-only variant.
func summaryLine(attr, kind string, totalLines, changedLines int) string {
	return fmt.Sprintf("~ %s · %s · %d lines · %d changed (hidden to fit size limit)",
		attr, kind, totalLines, changedLines)
}

// --- dispatcher ---

// Input is everything Diff needs for one attribute.
type Input struct {
	ResourceType string
	Attr         string
	Before       any
	After        any
	Sensitive    bool
	// BeforeSensitive/AfterSensitive carry Terraform's per-path sensitivity tree
	// for this attribute (bool, or a nested map/list mirroring the value). A
	// structural diff consults them to redact only the sensitive sub-paths, so a
	// deep sensitive leaf no longer smears "(sensitive value)" across the block.
	BeforeSensitive any
	AfterSensitive  any
	Unknown         bool
	ForceDiffer     string // "" | auto | structural | json | yaml | line | summary | hide
	MaxLines        int    // 0 = no cap
	NoDetect        bool   // when true, skip type sniffing for string values (force line diff)
	SensitivityOnly bool   // NEW!
}

// Diff builds the ordered variant ladder for one attribute.
func Diff(in Input) model.Field {
	f := diffInternal(in)
	f.SensitivityOnly = in.SensitivityOnly
	return f
}

func diffInternal(in Input) model.Field {
	// Always-inline cases → single leaf.
	switch {
	case in.Unknown:
		return leafField(in, model.OpChange, "(known after apply)")
	case in.Sensitive:
		return leafField(in, model.OpChange, "(sensitive value)")
	}

	bs, bIsStr := in.Before.(string)
	as, aIsStr := in.After.(string)

	// Create-only / delete-only scalar (one side nil).
	if in.Before == nil && in.After != nil && !isStructured(in.Before, in.After) {
		return scalarLeaf(in.Attr, model.OpAdd, "", scalar(in.After))
	}
	if in.After == nil && in.Before != nil && !isStructured(in.Before, in.After) {
		return scalarLeaf(in.Attr, model.OpRemove, scalar(in.Before), "")
	}

	// Forced "hide"/"summary" short-circuit (block).
	switch in.ForceDiffer {
	case "hide":
		return blockField(single(in.Attr, model.LevelHidden, ""))
	case "summary":
		return blockField(ladderFrom(in.Attr, model.LevelSummary, in))
	}

	// Native structured (maps/lists) → structural (Task 5 decides leaves vs block).
	if !bIsStr && !aIsStr && isStructured(in.Before, in.After) {
		return structural(in)
	}

	// Both scalar (non-string) → inline leaf.
	if !isStructured(in.Before, in.After) && !bIsStr && !aIsStr {
		return scalarLeaf(in.Attr, model.OpChange, scalar(in.Before), scalar(in.After))
	}

	// String values: detect → structural leaves (Task 5) or block.
	kind := in.ForceDiffer
	if kind == "" || kind == "auto" {
		if in.NoDetect {
			kind = "line"
		} else {
			switch detect(firstNonEmpty(bs, as)) {
			case typeJSON, typeYAML:
				return structural(in)
			case typeBase64:
				return blockField(ladderFrom(in.Attr, model.LevelSummary, in))
			default:
				kind = "line"
			}
		}
	}
	switch kind {
	case "structural", "json", "yaml":
		return structural(in)
	default: // line
		if !strings.Contains(bs, "\n") && !strings.Contains(as, "\n") && len(bs) < 60 && len(as) < 60 {
			return scalarLeaf(in.Attr, model.OpChange, fmt.Sprintf("%q", bs), fmt.Sprintf("%q", as))
		}
		return blockField(ladderFrom(in.Attr, model.LevelLineDiff, in))
	}
}

// leafField builds a one-leaf field whose value is rendered verbatim (markers).
func leafField(in Input, op model.LeafOp, marker string) model.Field {
	return model.Field{Name: in.Attr, Leaves: []model.Leaf{{Op: op, Path: in.Attr, Inline: marker}}}
}

// scalarLeaf builds a one-leaf field from already-rendered scalar strings.
func scalarLeaf(attr string, op model.LeafOp, old, nw string) model.Field {
	return model.Field{Name: attr, Leaves: []model.Leaf{{Op: op, Path: attr, Old: old, New: nw}}}
}

// blockField wraps a variant ladder (built by the existing helpers) as a Field.
func blockField(ad model.AttrDiff) model.Field {
	return model.Field{Name: ad.Name, Variants: ad.Variants}
}

// structural renders a map/JSON/YAML attribute as a contextual unified diff of
// its canonically-formatted value (2 lines of context, -/+ for changes). It is
// always a block (which fit can degrade to a summary), tagged with its kind.
func structural(in Input) model.Field {
	kind := structuredKind(in)
	bv := redactSensitive(parseStructured(in.Before, firstStr(in.Before)), in.BeforeSensitive)
	av := redactSensitive(parseStructured(in.After, firstStr(in.After)), in.AfterSensitive)
	before := canonical(bv, kind)
	after := canonical(av, kind)
	// No indent: the unified-diff lines must start with ' '/'-'/'+' so GitHub
	// colours them inside the ```diff fence.
	rich := contextDiff(before, after)
	total, changed := magnitude(before, after)

	var variants []model.Variant
	if in.MaxLines == 0 || lineCount(rich) <= in.MaxLines {
		variants = append(variants, variant(model.LevelStructural, rich))
	}
	variants = append(variants,
		variant(model.LevelSummary, "  "+summaryLine(in.Attr, kind, total, changed)),
		variant(model.LevelHidden, ""),
	)
	return model.Field{Name: in.Attr, Kind: kind, Variants: variants}
}

// sensitiveMarker is the placeholder substituted for a redacted leaf, matching
// Terraform's own rendering.
const sensitiveMarker = "(sensitive value)"

// redactSensitive returns a copy of v with every leaf that Terraform's
// sensitivity tree `s` marks sensitive replaced by sensitiveMarker. Terraform
// encodes the tree as `true` (the whole subtree is sensitive), a map mirroring
// an object's keys, or a list mirroring a tuple's elements; anything else
// (nil/false) means not sensitive. Replacing both sides with the same marker
// keeps a sensitive value from leaking while leaving non-sensitive siblings to
// diff normally.
func redactSensitive(v, s any) any {
	switch sm := s.(type) {
	case bool:
		if sm {
			return sensitiveMarker
		}
		return v
	case map[string]any:
		vm, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(vm))
		for k, val := range vm {
			out[k] = redactSensitive(val, sm[k])
		}
		return out
	case []any:
		va, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(va))
		for i, val := range va {
			var si any
			if i < len(sm) {
				si = sm[i]
			}
			out[i] = redactSensitive(val, si)
		}
		return out
	default: // nil / unrecognised → not sensitive
		return v
	}
}

// firstStr returns v as a string if it is one, else "".
func firstStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func isStructured(before, after any) bool {
	for _, v := range []any{before, after} {
		switch v.(type) {
		case map[string]any, []any:
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func single(attr string, lvl model.Level, content string) model.AttrDiff {
	return model.AttrDiff{
		Name:     attr,
		Variants: []model.Variant{{Level: lvl, Content: content, Bytes: len(content)}},
	}
}

// ladderFrom builds the full preferred→minimal ladder starting at `top`,
// honouring the optional MaxLines ceiling (which drops rich variants).
func ladderFrom(attr string, top model.Level, in Input) model.AttrDiff {
	bs, _ := in.Before.(string)
	as, _ := in.After.(string)

	var rich string
	var richLevel model.Level
	var kind string
	switch top {
	case model.LevelLineDiff:
		richLevel = model.LevelLineDiff
		kind = "text"
		rich = lineDiff(bs, as)
	case model.LevelSummary:
		total, changed := magnitude(bs, as)
		return model.AttrDiff{Name: attr, Variants: []model.Variant{
			variant(model.LevelSummary, "  "+summaryLine(attr, summaryKind(in, bs, as), total, changed)),
			variant(model.LevelHidden, ""),
		}}
	}

	total, changed := magnitude(bs, as)
	if total == 0 && rich != "" {
		// Native (non-string) structured values have empty bs/as, so magnitude
		// can't measure them; derive counts from the rendered diff instead.
		n := lineCount(rich)
		total, changed = n, n
	}
	richContent := indent(rich)
	richVariant := variant(richLevel, richContent)

	variants := []model.Variant{richVariant}
	if in.MaxLines > 0 && lineCount(richContent) > in.MaxLines {
		variants = nil
	}
	variants = append(variants,
		variant(model.LevelSummary, "  "+summaryLine(attr, kind, total, changed)),
		variant(model.LevelHidden, ""),
	)
	return model.AttrDiff{Name: attr, Variants: variants}
}

func variant(lvl model.Level, content string) model.Variant {
	return model.Variant{Level: lvl, Content: content, Bytes: len(content)}
}

// parseStructured returns native structured values; if the value is a string,
// it is parsed as JSON then YAML.
func parseStructured(native any, s string) any {
	switch native.(type) {
	case map[string]any, []any:
		return native
	}
	if s == "" {
		return native
	}
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	if yaml.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return native
}

func magnitude(before, after string) (total, changed int) {
	total = lineCount(after)
	if total == 0 {
		total = lineCount(before)
	}
	d := difflib.UnifiedDiff{A: difflib.SplitLines(before), B: difflib.SplitLines(after), Context: 0}
	out, _ := difflib.GetUnifiedDiffString(d)
	for _, ln := range strings.Split(out, "\n") {
		if (strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-")) &&
			!strings.HasPrefix(ln, "+++") && !strings.HasPrefix(ln, "---") {
			changed++
		}
	}
	return total, changed
}

func summaryKind(in Input, bs, as string) string {
	switch detect(firstNonEmpty(bs, as)) {
	case typeJSON:
		return "json"
	case typeYAML:
		return "yaml"
	case typeBase64:
		return "base64"
	default:
		return "text"
	}
}

func indent(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = "  " + ln
	}
	return strings.Join(lines, "\n")
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}
