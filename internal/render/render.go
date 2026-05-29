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
		cells := []string{s.Name}
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

func renderDetails(b *strings.Builder, r model.Report) {
	for _, s := range r.Stacks {
		if !s.Counts.AnyChange() {
			continue
		}
		summary := s.Name
		if s.Class != nil {
			summary += " · " + s.Class.Label()
		}
		summary += " · " + changeWord(s.Counts)
		open := ""
		if r.DetailsOpen {
			open = " open"
		}
		fmt.Fprintf(b, "\n<details%s><summary>%s</summary>\n", open, summary)
		for _, c := range s.Changes {
			renderResource(b, c)
		}
		b.WriteString("</details>\n")
	}
}

// renderResource emits one resource: a folded <details> for create/delete, or
// (for update/replace) an inline diff fence plus a folded <details> per block
// field.
func renderResource(b *strings.Builder, c model.Change) {
	switch c.Action {
	case model.ActionAdd, model.ActionDestroy:
		op := model.OpAdd
		if c.Action == model.ActionDestroy {
			op = model.OpRemove
		}
		var leaves []model.Leaf
		var blocks []model.Field
		for _, f := range c.Fields {
			if f.IsBlock() {
				blocks = append(blocks, f)
			} else {
				leaves = append(leaves, f.Leaves...)
			}
		}
		fmt.Fprintf(b, "\n<details><summary>%s %s · %d attrs</summary>\n\n```diff\n", op.Sym(), c.Address, len(c.Fields))
		for _, line := range alignLeaves(leaves) {
			b.WriteString(line)
			b.WriteString("\n")
		}
		for _, f := range blocks {
			v := f.Sel()
			if v.Level == model.LevelHidden || v.Content == "" {
				continue
			}
			fmt.Fprintf(b, "%s %s:\n%s\n", op.Sym(), f.Name, strings.TrimRight(v.Content, "\n"))
		}
		b.WriteString("```\n\n</details>\n")
		return
	}

	// ActionChange and ActionReplace: inline leaves, then block fields fold.
	suffix := ""
	if c.Action == model.ActionReplace {
		suffix = " · replace"
	}
	var inline []model.Leaf
	var blocks []model.Field
	for _, f := range c.Fields {
		if f.IsBlock() {
			blocks = append(blocks, f)
		} else {
			inline = append(inline, f.Leaves...)
		}
	}
	b.WriteString("\n```diff\n")
	fmt.Fprintf(b, "# %s%s\n", c.Address, suffix)
	for _, line := range alignLeaves(inline) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	for _, f := range blocks {
		v := f.Sel()
		if v.Level == model.LevelHidden || v.Content == "" {
			continue
		}
		lines := lineCountOf(v.Content)
		fmt.Fprintf(b, "\n<details><summary>~ %s · %d lines</summary>\n\n```diff\n%s\n```\n\n</details>\n", f.Name, lines, strings.TrimRight(v.Content, "\n"))
	}
}

// alignLeaves renders leaves as `op path = value`, padding paths so the `=`
// signs align.
func alignLeaves(leaves []model.Leaf) []string {
	w := 0
	for _, l := range leaves {
		if len(l.Path) > w {
			w = len(l.Path)
		}
	}
	out := make([]string, 0, len(leaves))
	for _, l := range leaves {
		pad := strings.Repeat(" ", w-len(l.Path))
		out = append(out, fmt.Sprintf("%s %s%s = %s", l.Op.Sym(), l.Path, pad, l.Value()))
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
	}
	line := fmt.Sprintf("%d stacks · %d adds · %d changes · %d destroys · %d replaces",
		len(r.Stacks), total.Add, total.Change, total.Destroy, total.Replace)
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
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
