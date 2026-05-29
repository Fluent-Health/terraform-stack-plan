// Package fit deterministically reduces a model.Report so its rendered size
// fits a byte budget, degrading the largest attribute diffs first, then
// falling back through a report-level terminal cascade.
package fit

import (
	"fmt"

	"github.com/Fluent-Health/terraform-stack-plan/internal/model"
	"github.com/Fluent-Health/terraform-stack-plan/internal/render"
)

// Fit mutates r in place. budget <= 0 disables reduction. It returns whether
// the rendered report fits within budget.
func Fit(r *model.Report, budget int) (fits bool) {
	if budget <= 0 {
		return true
	}
	// Phase 1: per-attribute largest-first degradation.
	for size(r) > budget {
		a := largestDegradable(r)
		if a == nil {
			break // everything minimal; fall to cascade
		}
		a.Selected++
	}
	if size(r) <= budget {
		return true
	}

	// Phase 2: terminal cascade. Capture the pre-cascade size for the "needs"
	// figure before mode changes shrink the rendered output.
	need := size(r)

	r.Mode = model.ModeSummaryOnly
	r.Notice = "⚠️ Per-stack detail omitted to fit GitHub's size limit (see CI logs / artifact)."
	if size(r) <= budget {
		return true
	}

	r.Mode = model.ModeMinimal
	r.Notice = fmt.Sprintf("⚠️ Per-stack table omitted: report needs ~%s, budget %s.", humanBytes(need), humanBytes(budget))
	// Best-effort floor: emit minimal regardless of whether it fits.
	return size(r) <= budget
}

// humanBytes formats a byte count as "N B" under 1KB, else "N KB".
func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%d KB", n/1024)
}

// size renders r and returns its byte length.
func size(r *model.Report) int { return len(render.Render(*r)) }

// largestDegradable returns a pointer to the not-yet-minimal attribute with the
// largest current variant. Iteration is in (stack, change, attr) order and ties
// keep the earliest, so selection is deterministic.
func largestDegradable(r *model.Report) *model.AttrDiff {
	var best *model.AttrDiff
	bestBytes := -1
	for si := range r.Stacks {
		for ci := range r.Stacks[si].Changes {
			attrs := r.Stacks[si].Changes[ci].Attrs
			for ai := range attrs {
				a := &attrs[ai]
				if a.AtLast() {
					continue
				}
				if b := a.Sel().Bytes; b > bestBytes {
					best = a
					bestBytes = b
				}
			}
		}
	}
	return best
}
