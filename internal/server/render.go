package server

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/ansi"
	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

// pamConsoleURL deep-links to the GCP PAM "Approve grants → Pending approval"
// tab for a target. The iam class emits the target as a GCP project
// (emit_attributes ["project"]), so the approver lands on that project's
// Privileged Access Manager approvals tab, where the pending grant is approved.
// GCP PAM exposes no per-grant deep link (only this project-scoped tab), so the
// link stops at the project. (gcp-pam is the only backend today; if more are
// added this should move behind the approval.Backend.)
func pamConsoleURL(target string) string {
	return "https://console.cloud.google.com/iam-admin/pam/grants/approvals?project=" + url.QueryEscape(target)
}

// gatesSection renders an "awaiting approval" banner for the check run when any
// gate target is not yet ACTIVE, with a deep link to approve each in PAM. Returns
// "" when every gate is approved (or there are none).
func gatesSection(targets []store.GateTarget) string {
	var pending []store.GateTarget
	for _, t := range targets {
		if t.State != "ACTIVE" {
			pending = append(pending, t)
		}
	}
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### ⏳ Awaiting approval\n\nThis plan changes gated resources. Merge is blocked until each grant is approved in PAM:\n\n")
	for _, t := range pending {
		fmt.Fprintf(&b, "- 🔐 `%s` · `%s` — [approve in PAM ↗](%s)\n", t.Class, t.Target, pamConsoleURL(t.Target))
	}
	b.WriteString("\n---\n\n")
	return b.String()
}

// done reports whether a stack has finished its work for progress accounting.
func done(s events.Status) bool {
	switch s {
	case events.StatusPlanned, events.StatusSafe, events.StatusNochange,
		events.StatusMoving, events.StatusGated, events.StatusFailed, events.StatusAborted:
		return true
	default:
		return false
	}
}

// opTally formats the non-zero mutating-op kinds of a verdict as a compact
// blast-radius string: "+A ~C ±R −D ↔M" (only the kinds present). "" when none.
func opTally(v verdict) string {
	var parts []string
	if v.Add > 0 {
		parts = append(parts, fmt.Sprintf("+%d", v.Add))
	}
	if v.Change > 0 {
		parts = append(parts, fmt.Sprintf("~%d", v.Change))
	}
	if v.Replace > 0 {
		parts = append(parts, fmt.Sprintf("±%d", v.Replace))
	}
	if v.Destroy > 0 {
		parts = append(parts, fmt.Sprintf("−%d", v.Destroy))
	}
	if v.Move > 0 {
		parts = append(parts, fmt.Sprintf("↔%d", v.Move))
	}
	return strings.Join(parts, " ")
}

// progressCells is the width of the unicode progress bar.
const progressCells = 10

// progress maps (phase, planned, initialized, total) to a 10-cell unicode bar,
// a human label, and a percentage. Pre-plan phases (warming/initializing) have
// no sub-progress by default; once stacks finish init the init band sub-fills
// (5–15%) and the label reads "initialized k/N". Planning fills the remaining
// 80% by the completed-stack fraction; an apply context tracks planned/total.
func progress(phases []config.PhaseWeight, phase events.Phase, planned, initialized, total int) (bar, label string, pct int) {
	label = phaseLabel(phase, planned, initialized, total)
	var frac float64
	if f, ok := weightedFrac(phases, phase, planned, initialized, total); ok {
		frac = f
	} else {
		frac = legacyFrac(phase, planned, initialized, total)
	}
	bar, pct = progressBar(frac)
	return bar, label, pct
}

// weightedFrac computes the full-progress fraction across a configured, ordered
// weighted phase set: completed phases contribute their whole weight, the
// current ticking phase sub-fills its band by completed/total, and a current
// marker phase counts as its full band (markers are instantaneous). Returns
// (_, false) when phases is empty or the phase is not in the set (caller falls
// back to the built-in fractions).
func weightedFrac(phases []config.PhaseWeight, phase events.Phase, planned, initialized, total int) (float64, bool) {
	if len(phases) == 0 {
		return 0, false
	}
	var totalW, cum float64
	for _, pw := range phases {
		totalW += pw.Weight
	}
	found := false
	for _, pw := range phases {
		if pw.Phase == phase {
			sub := 1.0 // marker phase: full band once entered
			if phase.Ticking() && total > 0 {
				n := planned
				if phase == events.PhaseInitializing {
					n = initialized
				}
				sub = float64(n) / float64(total)
			}
			cum += pw.Weight * sub
			found = true
			break
		}
		cum += pw.Weight
	}
	if !found || totalW <= 0 {
		return 0, false
	}
	return cum / totalW, true
}

// legacyFrac is the built-in bar fraction used when no progress{} block is
// configured (preserves the original behavior; lint/test alias warming/verify).
func legacyFrac(phase events.Phase, planned, initialized, total int) float64 {
	switch phase {
	case events.PhaseWarming, events.PhaseLinting:
		return 0.05
	case events.PhaseInitializing:
		if total > 0 && initialized > 0 {
			return 0.05 + 0.10*float64(initialized)/float64(total)
		}
		return 0.15
	case events.PhaseApplying:
		if total > 0 {
			return float64(planned) / float64(total)
		}
		return 0
	case events.PhaseTesting, events.PhaseVerifying:
		return 1.0
	default: // planning, or an unset phase treated as planning
		switch {
		case total <= 0:
			return 0.20
		case planned >= total:
			return 1.0
		default:
			return 0.20 + 0.80*float64(planned)/float64(total)
		}
	}
}

// phaseLabel is the human label for a phase + counts, shared by the weighted and
// built-in fraction paths.
func phaseLabel(phase events.Phase, planned, initialized, total int) string {
	switch phase {
	case events.PhaseWarming:
		return "warming cache…"
	case events.PhaseLinting:
		return "linting modules…"
	case events.PhaseInitializing:
		if total > 0 && initialized > 0 {
			return fmt.Sprintf("initialized %d/%d", initialized, total)
		}
		return fmt.Sprintf("initializing %d stacks…", total)
	case events.PhaseApplying:
		if total > 0 && planned == total {
			return fmt.Sprintf("applied %d/%d", planned, total)
		}
		return fmt.Sprintf("applying %d/%d", planned, total)
	case events.PhaseTesting:
		return "testing…"
	case events.PhaseVerifying:
		return "verifying…"
	default: // planning
		switch {
		case total <= 0:
			return "planning…"
		case planned >= total:
			return "planned"
		default:
			return fmt.Sprintf("planning %d/%d", planned, total)
		}
	}
}

// progressBar renders the unicode fill bar + integer percentage for a 0..1 fraction.
// It uses 6-dot Braille characters to fill columns bottom-to-top, left-to-right.
func progressBar(frac float64) (bar string, pct int) {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}

	const progressCells = 10
	val := frac * float64(progressCells)
	fullCells := int(val)
	rem := val - float64(fullCells)

	// Map remainder to 6 sub-states (0 to 6)
	index := int(rem*6.0 + 0.5)
	if index == 6 {
		fullCells++
		index = 0
	}

	var b strings.Builder
	// Write full cells: '⠿' (both columns full)
	b.WriteString(strings.Repeat("⠿", fullCells))

	// Write fractional cell if any
	if index > 0 && fullCells < progressCells {
		// 1: bottom-left (⠄)
		// 2: bottom-left + middle-left (⠆)
		// 3: left column full (⠇)
		// 4: left column full + bottom-right (⠧)
		// 5: left column full + bottom-right + middle-right (⠷)
		subBlocks := []string{"⠄", "⠆", "⠇", "⠧", "⠷"}
		b.WriteString(subBlocks[index-1])
	}

	// Calculate remaining empty cells to maintain exact length of progressCells
	// Empty cell in Braille is U+2800 (⠀), which ensures same character width
	emptyCells := progressCells - fullCells
	if index > 0 {
		emptyCells--
	}
	if emptyCells > 0 {
		b.WriteString(strings.Repeat("⠀", emptyCells))
	}

	return b.String(), int(frac*100 + 0.5)
}

// checkSummary builds the check-run summary body for a plan or apply: verdict
// chips (destructive / IAM) + a live-viewer link, then a per-stack table
// (Stack | Ops | Risk | State). kind is "plan" or "apply". The GitHub check-run
// title already carries the status line (e.g. "Apply · applied 2/8 · …"), so the
// summary deliberately does NOT repeat it as a headline.
func checkSummary(kind string, stacks []events.StackState, viewerURL string, phase events.Phase) string {
	var b strings.Builder

	// Verdict chips + viewer link.
	var chips []string
	destructive := false
	for _, s := range stacks {
		for _, rt := range riskTags(s) {
			if rt.CSS == "danger" {
				destructive = true
			}
		}
	}
	if destructive {
		chips = append(chips, "⚠️ destructive")
	}
	if n := iamCount(stacks); n > 0 {
		chips = append(chips, fmt.Sprintf("⚿ %d IAM", n))
	}
	if viewerURL != "" {
		chips = append(chips, fmt.Sprintf("[live viewer ↗](%s)", viewerURL))
	}
	if len(chips) > 0 {
		b.WriteString(strings.Join(chips, " · ") + "\n")
	}
	b.WriteString("\n")

	// Per-stack table (omitted before any stack is registered, e.g. warming).
	if len(stacks) > 0 {
		b.WriteString("| Stack | Ops | Risk | State |\n|---|---|---|---|\n")
		for _, s := range stacks {
			var risks []string
			for _, rt := range riskTags(s) {
				risks = append(risks, rt.Label)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n",
				s.Path, opSummary(s.Counts), strings.Join(risks, " "), displayState(s.Status, kind, phase).Label)
		}
	}
	return b.String()
}

// errorTail extracts the most relevant trailing slice of a terraform log excerpt
// for a failure summary: the last "╷ … ╵" diagnostic block (rule 1, returned in full),
// else from the last line containing "Error:" to the end (rule 2, returned in full),
// else the last maxLines non-blank lines (rule 3 fallback, capped). Trailing blank
// lines are trimmed. "" for blank input.
func errorTail(excerpt string, maxLines int) string {
	excerpt = ansi.Strip(excerpt)
	excerpt = strings.TrimRight(excerpt, "\n \t")
	if strings.TrimSpace(excerpt) == "" {
		return ""
	}
	lines := strings.Split(excerpt, "\n")

	var picked []string
	// 1. last ╷ … ╵ diagnostic block.
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "╷") {
			start = i
			break
		}
	}
	switch {
	case start >= 0:
		end := len(lines)
		for j := start; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "╵") {
				end = j + 1
				break
			}
		}
		picked = lines[start:end]
	default:
		// 2. last "Error:" to EOF.
		errIdx := -1
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "Error:") {
				errIdx = i
				break
			}
		}
		if errIdx >= 0 {
			picked = lines[errIdx:]
		} else {
			// 3. last maxLines non-blank lines.
			for i := len(lines) - 1; i >= 0 && len(picked) < maxLines; i-- {
				if strings.TrimSpace(lines[i]) == "" {
					continue
				}
				picked = append([]string{lines[i]}, picked...)
			}
			// Cap only applies to rule 3 fallback.
			if len(picked) > maxLines {
				picked = picked[len(picked)-maxLines:]
			}
		}
	}
	return strings.TrimRight(strings.Join(picked, "\n"), "\n")
}

// failuresSection renders a collapsible block per failed stack with the captured
// error detail (which, for an apply, names the phase it died in — terraform init
// vs apply) and links. logURL is the CI build log; stackLogPrefix, when non-empty
// (apply context), is the per-execution log base (e.g. "<base>/logs/<exec>") used
// to deep-link each failing stack's own streamed log at "<prefix>/<stack>".
// Returns "" when nothing failed.
func failuresSection(g events.Graph, logURL, stackLogPrefix string) string {
	var failed []events.StackState
	for _, s := range g.Stacks {
		if s.Status == events.StatusFailed {
			failed = append(failed, s)
		}
	}
	if len(failed) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### ❌ Failures (%d)\n\n", len(failed))
	if logURL != "" {
		fmt.Fprintf(&b, "🔗 [Full build log](%s)\n\n", logURL)
	}
	for _, s := range failed {
		fmt.Fprintf(&b, "<details><summary><code>%s</code></summary>\n\n", s.Path)
		if s.Detail != "" {
			fmt.Fprintf(&b, "```\n%s\n```\n", s.Detail)
			// Triage is keyed off the captured error, so only emit it when there
			// is one — with no detail the "_see the build log_" note below is the
			// only honest advice (the generic steps would say "read the error").
			tr := classifyFailure(s.Detail, s.Categories)
			if tr.Cause != "" {
				fmt.Fprintf(&b, "\n**Likely cause.** %s\n", tr.Cause)
			}
			if len(tr.Steps) > 0 {
				b.WriteString("\n**Next steps:**\n\n")
				for i, step := range tr.Steps {
					fmt.Fprintf(&b, "%d. %s\n", i+1, step)
				}
			}
			if tr.StateImpact != "" {
				fmt.Fprintf(&b, "\n**State impact.** %s\n", tr.StateImpact)
			}
		} else {
			b.WriteString("_No error detail captured — see the build log._\n")
		}
		if stackLogPrefix != "" {
			fmt.Fprintf(&b, "\n📄 [Stack log](%s/%s)\n", strings.TrimRight(stackLogPrefix, "/"), s.Path)
		}
		b.WriteString("\n</details>\n\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}
