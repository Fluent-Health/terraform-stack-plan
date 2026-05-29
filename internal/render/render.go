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
		fmt.Fprintf(b, "\n<details%s><summary>%s</summary>\n\n", open, summary)
		b.WriteString("```diff\n")
		for _, c := range s.Changes {
			renderChange(b, c)
		}
		b.WriteString("```\n\n</details>\n")
	}
}

func renderChange(b *strings.Builder, c model.Change) {
	switch c.Action {
	case model.ActionAdd:
		fmt.Fprintf(b, "+ %s\n", c.Address)
		return
	case model.ActionDestroy:
		fmt.Fprintf(b, "- %s\n", c.Address)
		return
	}
	verb := "will be updated in-place"
	if c.Action == model.ActionReplace {
		verb = "will be replaced"
	}
	fmt.Fprintf(b, "# %s %s\n", c.Address, verb)
	for _, a := range c.Attrs {
		v := a.Sel()
		if v.Level == model.LevelHidden || v.Content == "" {
			continue
		}
		b.WriteString(v.Content)
		if !strings.HasSuffix(v.Content, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
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
