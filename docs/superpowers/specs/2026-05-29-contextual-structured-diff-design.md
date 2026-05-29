# Contextual diffs for structured values

**Date:** 2026-05-29
**Status:** Approved (design)
**Builds on:** PR #2 branch (`render/per-resource-nesting`).

## Problem

Structured changes (JSON/YAML strings and native HCL maps/lists) render as a
changed-paths-only list (`~ policy.Statement[0].Resource = "old" → "new"`). It's
terse but hard to read for real policies/manifests — no surrounding context.

## Goal (user choice: B)

Render **all structured values** as a contextual unified diff inside a ```diff
block: the value canonically re-formatted, **2 lines of context** around each
change, changed lines shown as `-`/`+` (an update = a `-` line and a `+` line).
Applies to JSON strings, YAML strings, **and** native HCL maps/lists.

## Design

### Differ (`internal/differ`)

Replace the structural changed-paths rendering with a contextual diff. The
`structural(in)` path always produces a **block** (no more small-inline
structured leaves); leaves remain only for scalars / sensitive / unknown.

For a structured attribute:
1. Parse both sides to `any` (native value, or JSON-then-YAML for strings — the
   existing `parseStructured`).
2. Pick a canonical `kind`: a JSON-detected string → `json`; a YAML-detected
   string → `yaml`; a native map/list → `yaml` (clean, low-noise default).
3. Canonicalize each side to text — `json`: `json.MarshalIndent(v,"","  ")`;
   `yaml`: `yaml.Marshal(v)` (sorted keys, stable indent). A `nil` side → `""`
   (so a create renders as all-`+`, a delete as all-`-`).
4. Unified diff with **context = 2**; drop the `---`/`+++` file headers; replace
   each hunk's `@@ … @@` header with a `⋮` separator line (omit before the first
   hunk). Keep ` `/`-`/`+` lines.

This becomes the block's **rich** variant. The ladder stays
`[rich, Summary, Hidden]` so `fit` can still degrade a huge diff to a one-line
summary (`~ attr · kind · N lines · M changed (hidden to fit size limit)`), and
the per-resource size rule (PR #2) still folds a big diff to a closed row.

`structuralDiff` / `structuralLeaves` / `firstStr`-only-for-leaves and the
`foldThreshold` leaf cutoff are removed (superseded). `parseStructured`,
`flatten` (if still needed by canonicalization — it is not), and the scalar/
line/base64 paths are unchanged.

### Model + render

`model.Field` gains `Kind string` (`"json"`/`"yaml"`/`""`). The differ sets it
on structured blocks. Render's block header becomes `~ <name> (<kind>):` when
`Kind != ""`, else `~ <name>:`. Everything else in render is unchanged — a
structured block is just another block in the resource body.

### Examples

Regenerate goldens. `state-ops.md`'s JSON/YAML cases now show contextual diffs;
the native nested-block (`google_compute_firewall.web`) now also renders as a
YAML contextual diff (B). Retune the cascade budgets in `big-plan.md`'s siblings
if the larger structured output shifts mode boundaries; the four invariants must
still hold. README's structured note updated to describe the contextual diff.

## Scope / affected components

| Package | Change |
|---------|--------|
| `internal/differ` | `structural` → contextual diff block; new `contextDiff`/`canonical`/`structuredKind`; keep `parseStructured`; remove `structuralDiff`, `structuralLeaves`, `foldThreshold`. |
| `internal/model` | `Field.Kind string`. |
| `internal/render` | block header shows `(kind)`. |
| `internal/fit` | none (still degrades block variants). |
| `cmd/tfstackplan` | regenerate goldens; retune budgets. |
| `README.md` | structured-diff note. |

## Testing

- differ: a small structured change → contextual diff with 2 context lines and a
  `-`/`+` pair (assert a context line and the `-`/`+` lines present); a multi-hunk
  change → a `⋮` separator; native map and JSON string and YAML string each
  produce the right `kind`; create (nil before) → all-`+`; the block still
  ladders to Summary/Hidden.
- render: block header shows `(json)`/`(yaml)`.
- examples: regenerated; cascade invariants hold; assert a context line (leading
  space) appears in a structured diff in `state-ops.md`.

## Risks

- **Budget retuning** — contextual diffs are larger than changed-paths lists, so
  more content per structured change; the cascade example budgets likely need a
  bump. Invariant assertions catch boundary slips.
- **Canonical reformatting** changes line content vs the provider's original
  text; acceptable (and clearer) — we already parse/re-render structured values.
- **Very wide values** (long single lines) aren't wrapped; same as today.
