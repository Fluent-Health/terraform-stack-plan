// Package differ turns a single attribute's before/after values into an ordered
// list of render variants (preferred → minimal). All value-type knowledge lives
// here; fit and render treat the result generically.
package differ

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
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

// flatten produces dotted leaf paths for a parsed value.
func flatten(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, val, out)
		}
	case []any:
		for i, val := range t {
			flatten(fmt.Sprintf("%s[%d]", prefix, i), val, out)
		}
	default:
		out[prefix] = scalar(v)
	}
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

// structuralDiff renders only the changed/added/removed leaf paths.
func structuralDiff(before, after any) string {
	bm, am := map[string]string{}, map[string]string{}
	flatten("", before, bm)
	flatten("", after, am)

	keys := map[string]struct{}{}
	for k := range bm {
		keys[k] = struct{}{}
	}
	for k := range am {
		keys[k] = struct{}{}
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var b strings.Builder
	for _, k := range sorted {
		bv, bok := bm[k]
		av, aok := am[k]
		switch {
		case bok && aok && bv != av:
			fmt.Fprintf(&b, "~ %s: %s -> %s\n", k, unquote(bv), unquote(av))
		case bok && !aok:
			fmt.Fprintf(&b, "- %s: %s\n", k, unquote(bv))
		case !bok && aok:
			fmt.Fprintf(&b, "+ %s: %s\n", k, unquote(av))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// unquote strips the surrounding quotes scalar() adds to strings, for readability.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
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
	Unknown      bool
	ForceDiffer  string // "" | auto | structural | json | yaml | line | summary | hide
	MaxLines     int    // 0 = no cap
	NoDetect     bool   // when true, skip type sniffing for string values (force line diff)
}

// Diff builds the ordered variant ladder for one attribute.
func Diff(in Input) model.Field {
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

// structural renders a map/JSON/YAML attribute. Task 5 turns small diffs into
// leaves; for now it always produces the block ladder.
func structural(in Input) model.Field {
	return blockField(ladderFrom(in.Attr, model.LevelStructural, in))
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
	case model.LevelStructural:
		richLevel = model.LevelStructural
		kind = "structured"
		rich = structuralDiff(parseStructured(in.Before, bs), parseStructured(in.After, as))
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
