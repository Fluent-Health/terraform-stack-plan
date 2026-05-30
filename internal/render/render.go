// Package render turns a (possibly fit-reduced) model.Report into the final
// markdown document. It is pure and deterministic.
package render

import (
	"fmt"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
)

// Render produces the full markdown document for r.
func Render(r model.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- %s -->\n", r.Marker)

	// Nothing changed: heading ("(0 stacks changed)") + header links only —
	// no summary table, no details. Reachable via an empty plans-dir.
	if len(r.Stacks) == 0 {
		renderHeader(&b, r)
		return b.String()
	}

	switch r.Mode {
	case model.ModeMinimal:
		renderMinimal(&b, r)
		return b.String()
	case model.ModeSummaryOnly:
		renderHeader(&b, r)
		renderTable(&b, r)
		if r.Notice != "" {
			fmt.Fprintf(&b, "\n%s\n", r.Notice)
		}
		return b.String()
	default:
		renderHeader(&b, r)
		renderTable(&b, r)
		if r.Notice != "" {
			fmt.Fprintf(&b, "\n%s\n", r.Notice)
		}
		renderDetails(&b, r)
		return b.String()
	}
}

func changedStacks(r model.Report) int {
	n := 0
	for _, s := range r.Stacks {
		if s.Counts.AnyChange() {
			n++
		}
	}
	return n
}

func renderHeader(b *strings.Builder, r model.Report) {
	fmt.Fprintf(b, "### %s  (%d stacks changed)\n\n", r.Title, changedStacks(r))
	if len(r.HeaderLinks) > 0 {
		parts := make([]string, 0, len(r.HeaderLinks))
		for _, l := range r.HeaderLinks {
			parts = append(parts, fmt.Sprintf("[%s](%s)", l.Label, l.URL))
		}
		fmt.Fprintf(b, "%s\n\n", strings.Join(parts, " · "))
	}
}

type columnSet struct{ add, change, destroy, replace bool }

func columns(r model.Report) columnSet {
	var cs columnSet
	for _, s := range r.Stacks {
		cs.add = cs.add || s.Counts.Add > 0
		cs.change = cs.change || s.Counts.Change > 0
		cs.destroy = cs.destroy || s.Counts.Destroy > 0
		cs.replace = cs.replace || s.Counts.Replace > 0
	}
	return cs
}

func renderTable(b *strings.Builder, r model.Report) {
	cs := columns(r)
	headers := []string{"Stack"}
	if cs.add {
		headers = append(headers, "Add")
	}
	if cs.change {
		headers = append(headers, "Change")
	}
	if cs.destroy {
		headers = append(headers, "Destroy")
	}
	if cs.replace {
		headers = append(headers, "Replace")
	}
	if r.Classified {
		headers = append(headers, "Class")
	}

	fmt.Fprintf(b, "| %s |\n", strings.Join(headers, " | "))
	aligns := make([]string, len(headers))
	for i, h := range headers {
		switch h {
		case "Stack", "Class":
			aligns[i] = "---"
		default:
			aligns[i] = "---:"
		}
	}
	fmt.Fprintf(b, "| %s |\n", strings.Join(aligns, " | "))

	for _, s := range r.Stacks {
		// Table choice B: move/import/forget have no columns; surface them as a
		// dim suffix on the Stack cell instead.
		name := s.Name
		if extra := strings.Join(extrasParts(s.Counts), ", "); extra != "" {
			name += " · " + extra
		}
		cells := []string{name}
		if cs.add {
			cells = append(cells, itoa(s.Counts.Add))
		}
		if cs.change {
			cells = append(cells, itoa(s.Counts.Change))
		}
		if cs.destroy {
			cells = append(cells, itoa(s.Counts.Destroy))
		}
		if cs.replace {
			cells = append(cells, itoa(s.Counts.Replace))
		}
		if r.Classified {
			label := ""
			if s.Class != nil {
				label = s.Class.Label()
			}
			cells = append(cells, label)
		}
		fmt.Fprintf(b, "| %s |\n", strings.Join(cells, " | "))
	}
}

// openThreshold is the rendered-body line count at or below which a resource's
// <details> row is open by default; larger bodies collapse to a row you expand.
const openThreshold = 10

func renderDetails(b *strings.Builder, r model.Report) {
	for _, s := range r.Stacks {
		if !s.Counts.AnyChange() {
			continue
		}
		name := s.Name
		if s.URL != "" {
			name = fmt.Sprintf("<a href=%q>%s</a>", s.URL, s.Name)
		}
		summary := name
		if s.Class != nil {
			summary += " · " + s.Class.Label()
		}
		summary += " · " + changeWord(s.Counts)
		open := ""
		if r.DetailsOpen {
			open = " open"
		}
		fmt.Fprintf(b, "\n<details%s><summary>%s</summary>\n\n", open, summary)
		var body strings.Builder
		for _, c := range s.Changes {
			renderResource(&body, c, r.DetailsOpen)
		}
		// Wrap the resource rows in a blockquote so GitHub draws an indented
		// left bar marking the stack scope ("you are inside this stack").
		b.WriteString(blockquote(body.String()))
		b.WriteString("\n</details>\n")
	}
}

// blockquote prefixes every line with "> " (blank lines become ">") so GitHub
// renders the block as an indented, left-bordered quote.
func blockquote(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + ln
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// renderResource emits one resource as a uniform <details> row. The row is open
// when its body is small (<= openThreshold lines) or forceOpen is set; otherwise
// it collapses to a summary line. The body holds the aligned leaf changes
// followed by any block-field diffs.
func renderResource(b *strings.Builder, c model.Change, forceOpen bool) {
	sym := fieldSym(c.Action)

	var leaves []model.Leaf
	var blocks []model.Field
	for _, f := range c.Fields {
		if f.IsBlock() {
			blocks = append(blocks, f)
		} else {
			leaves = append(leaves, f.Leaves...)
		}
	}

	// forget bodies use the ⊘ glyph so removed-from-state attributes read
	// distinctly from a destroy's "-".
	forceSym := ""
	if c.Action == model.ActionForget {
		forceSym = "⊘"
	}

	var body strings.Builder
	for _, line := range alignLeaves(leaves, forceSym) {
		body.WriteString(line)
		body.WriteString("\n")
	}
	for _, f := range blocks {
		v := f.Sel()
		if v.Level == model.LevelHidden || v.Content == "" {
			continue
		}
		hdr := f.Name
		if f.Kind != "" {
			hdr = fmt.Sprintf("%s (%s)", f.Name, f.Kind)
		}
		fmt.Fprintf(&body, "%s %s:\n%s\n", sym, hdr, strings.TrimRight(v.Content, "\n"))
	}
	content := strings.TrimRight(body.String(), "\n")
	if content == "" {
		switch {
		case c.Moved:
			content = "(address change only)"
		case c.Imported:
			content = "(import only)"
		}
	}

	open := ""
	if forceOpen || lineCountOf(content) <= openThreshold {
		open = " open"
	}
	fmt.Fprintf(b, "<details%s><summary>%s</summary>\n\n```diff\n%s\n```\n\n</details>\n",
		open, resourceSummary(c), content)
}

// resourceSummary is the one-line row label: glyph + address + magnitude. State
// operations take precedence over the underlying action: forget → moved →
// imported → create/update/delete/replace.
func resourceSummary(c model.Change) string {
	addr := c.Address
	if c.URL != "" {
		addr = fmt.Sprintf("<a href=%q>%s</a>", c.URL, c.Address)
	}
	n := len(c.Fields)
	switch {
	case c.Action == model.ActionForget:
		return fmt.Sprintf("⊘ %s · forgotten · %d attrs", addr, n)
	case c.Moved:
		s := fmt.Sprintf("↪ %s · moved from %s", addr, c.PreviousAddress)
		if n > 0 {
			s += fmt.Sprintf(", %d changed", n)
		}
		return s
	case c.Imported:
		s := fmt.Sprintf("⤓ %s · imported", addr)
		if c.ImportID != "" {
			s = fmt.Sprintf("⤓ %s · imported (id=%q)", addr, c.ImportID)
		}
		if n > 0 {
			s += fmt.Sprintf(", %d changed", n)
		}
		return s
	case c.Action == model.ActionAdd:
		return fmt.Sprintf("+ %s · %d attrs", addr, n)
	case c.Action == model.ActionDestroy:
		return fmt.Sprintf("- %s · %d attrs", addr, n)
	case c.Action == model.ActionReplace:
		return fmt.Sprintf("± %s · replace", addr)
	default:
		return fmt.Sprintf("~ %s · %d changed", addr, n)
	}
}

// fieldSym is the diff glyph used to label a block field's body within a
// resource of the given action.
func fieldSym(a model.Action) string {
	switch a {
	case model.ActionAdd:
		return "+"
	case model.ActionDestroy:
		return "-"
	case model.ActionForget:
		return "⊘"
	default:
		return "~"
	}
}

// alignLeaves renders leaves as "op path = value", padding paths so the "="
// signs align. A non-empty forceSym overrides each leaf's own op glyph (used by
// forget rows to mark every attribute with ⊘).
func alignLeaves(leaves []model.Leaf, forceSym string) []string {
	w := 0
	for _, l := range leaves {
		if len(l.Path) > w {
			w = len(l.Path)
		}
	}
	out := make([]string, 0, len(leaves))
	for _, l := range leaves {
		pad := strings.Repeat(" ", w-len(l.Path))
		sym := l.Op.Sym()
		if forceSym != "" {
			sym = forceSym
		}
		out = append(out, fmt.Sprintf("%s %s%s = %s", sym, l.Path, pad, l.Value()))
	}
	return out
}

func lineCountOf(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

func renderMinimal(b *strings.Builder, r model.Report) {
	var total model.Counts
	for _, s := range r.Stacks {
		total.Add += s.Counts.Add
		total.Change += s.Counts.Change
		total.Destroy += s.Counts.Destroy
		total.Replace += s.Counts.Replace
		total.Import += s.Counts.Import
		total.Move += s.Counts.Move
		total.Forget += s.Counts.Forget
	}
	line := fmt.Sprintf("%d stacks · %d adds · %d changes · %d destroys · %d replaces",
		len(r.Stacks), total.Add, total.Change, total.Destroy, total.Replace)
	if total.Import+total.Move+total.Forget > 0 {
		line += fmt.Sprintf(" · %d imports · %d moves · %d forgets", total.Import, total.Move, total.Forget)
	}
	fmt.Fprintf(b, "### %s\n\n%s\n", r.Title, line)
	if r.Notice != "" {
		fmt.Fprintf(b, "\n%s\n", r.Notice)
	}
}

func changeWord(c model.Counts) string {
	parts := []string{}
	if c.Add > 0 {
		parts = append(parts, fmt.Sprintf("%d add", c.Add))
	}
	if c.Change > 0 {
		parts = append(parts, fmt.Sprintf("%d change", c.Change))
	}
	if c.Destroy > 0 {
		parts = append(parts, fmt.Sprintf("%d destroy", c.Destroy))
	}
	if c.Replace > 0 {
		parts = append(parts, fmt.Sprintf("%d replace", c.Replace))
	}
	parts = append(parts, extrasParts(c)...)
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// extrasParts returns the move/import/forget count phrases (empty when none).
func extrasParts(c model.Counts) []string {
	var parts []string
	if c.Import > 0 {
		parts = append(parts, fmt.Sprintf("%d import", c.Import))
	}
	if c.Move > 0 {
		parts = append(parts, fmt.Sprintf("%d move", c.Move))
	}
	if c.Forget > 0 {
		parts = append(parts, fmt.Sprintf("%d forget", c.Forget))
	}
	return parts
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
