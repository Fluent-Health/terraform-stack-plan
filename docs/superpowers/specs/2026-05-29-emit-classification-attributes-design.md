# Emit classification attributes

**Date:** 2026-05-29
**Status:** Approved (design)

## Problem

`--emit-classification-json` hands CI the computed class per stack as data, so a
pipeline can gate on it instead of re-parsing markdown. But it emits **only**
`{class, icon}`. Real gating often needs to know *which subjects* triggered the
class — most concretely, **which GCP projects have IAM changes**, to request a
just-in-time access grant per project before apply.

A downstream consumer (the Fluent Health `infra` repo) currently computes this
with a hand-written `jq` pass alongside the tool: it filters `resource_changes`
to IAM types, pulls each change's `.change.after.project // .change.before.project`,
drops nulls, and dedupes into a `target_projects` array that drives its PAM grant
loop. That logic duplicates the tool's own classification matcher and can drift
from it. The tool already knows which changes matched the IAM rule; it should be
able to surface their attributes as data so the consumer drops the parallel `jq`.

## Goal

Let a classification rule (or preset) declare a set of **attribute names** to
extract from the changes it matched, and emit their unique values per stack in
the `--emit-classification-json` sidecar.

Concretely, the `iam` preset declares `emit_attributes = ["project"]`; the
sidecar then carries, per IAM-classified stack, the sorted-unique set of
projects with IAM changes — replacing the consumer's `jq`-derived
`target_projects` byte-for-byte.

Non-goals: surfacing attributes in the rendered markdown, nested/non-scalar
attribute extraction, and any change to how the class itself is computed.

## Decisions (locked)

- **Generic, not IAM-specific.** A reusable `emit_attributes` list on any
  rule/preset, not a hard-coded `target_projects` field.
- **Matched changes only.** Attributes are extracted from the changes the
  *firing* rule matched — not the whole stack. This is the PAM semantics:
  "projects that have IAM changes". Extracting from all changes would, e.g.,
  request a grant on a project that only has a bucket change in the same stack —
  an over-grant and a privilege-surface regression versus the current `jq`,
  which pulls `project` only from the IAM-matched set.
- **Top-level scalar attributes only** in v1. `project` / `role` / `name` are
  all top-level scalars, so the motivating case is fully covered. Nested paths
  (`settings.0.tier`) are out of scope.
- **Sensitive values are never emitted.** A top-level scalar flagged sensitive
  is skipped, so the sidecar can't leak a secret.

## Design

### Config (HCL)

`emit_attributes` is an optional `list(string)` on **both** the `rule` and
`preset` blocks (presets currently accept only `icon`):

```hcl
classification {
  default { name = "safe" icon = "✅" }

  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }

  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
    # no emit_attributes → emits nothing extra
  }
}
```

The list attaches to whichever rule fires (first-hit, as today). The `default`
class has no rule, so a stack that falls through to `default` emits no
attributes.

### Output shape

The per-stack sidecar entry gains an optional `attributes` object, keyed by
attribute name → sorted-unique non-null string values:

```json
{
  "platform/nonprod": {
    "class": "iam",
    "icon": "🔐",
    "attributes": { "project": ["fh-host-nonprod"] }
  },
  "service-projects/app-dev": {
    "class": "safe",
    "icon": "✅"
  }
}
```

- `attributes` is **omitted** (`omitempty`), not `{}` or `null`, when the firing
  rule declares no `emit_attributes` or when every matched change yielded only
  nulls. Consumers that read only `class` / `icon` are unaffected.
- Values are stringified (numbers/bools → their JSON-scalar string) so the map
  is uniformly `map[string][]string`.

### Extraction semantics

For the firing rule's `emit_attributes`, over the **matched changes only**:

1. Read the attribute from the change's raw **`after`**, falling back to
   **`before`** (covers deletes, where `after` is null).
2. **Top-level scalar only** (string / number / bool). A missing key, a
   non-scalar value, or a sensitive value contributes nothing.
3. **Drop nulls, dedupe, sort** — matching the consumer's current
   `map(select(. != null)) | unique`.

So org/folder IAM resources (`google_organization_iam_binding`, which have no
`project` attribute) correctly contribute nothing to `project`, matching today's
`target_projects` exactly.

### Why the raw value, not the reduced model

`plan.Parse` reduces each change to its **changed** attributes only (an in-place
update keeps just the attrs whose value differs). So `project` is absent from
the reduced model on an in-place IAM binding update. Extraction therefore reads
a separately-retained raw value, populated for **every** change regardless of
whether it changed.

### Components

| Package | Change |
|---------|--------|
| `internal/plan` | `RawChange` gains `Raw map[string]any` — top-level scalar attributes only (string/number/bool), merged `after` over `before`, **skipping sensitive values**. Populated in `Parse` for every change (uses the before/after maps and sensitivity markers it already reads). |
| `internal/classify` | `Rule` gains `EmitAttributes []string`. `Classify` returns a new `Result{ Class model.Class; Attributes map[string][]string }` instead of a bare `model.Class`; when the firing rule has `EmitAttributes`, it extracts those from the rule's matched changes (after→before, nil-drop, sort-unique). Stays pure — no I/O, no config types. |
| `internal/presets` | `Get` accepts and propagates `emitAttributes` onto the returned `Rule`. The preset's matcher is unchanged; it only carries the list through. |
| `internal/config` | `presetBody` and `ruleBody` gain `EmitAttributes []string` (`hcl:"emit_attributes,optional"`), passed through to `classify.Rule`. |
| `cmd/tfstackplan` | `classEntry` gains `Attributes map[string][]string` (`json:"attributes,omitempty"`); populated from `Result`. The `classify.Classify` call-site is updated for the new return type. |

### Flow

```
parse  → RawChange.Raw populated (top-level scalars, sensitive skipped)
classify (per stack) → Result{Class, Attributes}   [Attributes from matched changes of firing rule]
cmd    → sidecar[name] = {class, icon, attributes?}  (attributes omitempty)
render → unchanged (markdown does not show attributes)
```

`Classify`'s signature change is internal and pre-1.0; no external API impact.
The markdown path and byte-budget (`fit`) are untouched.

## Testing

- `internal/plan`: `Raw` holds top-level scalars, including on an in-place
  update where the attribute is **unchanged** (the regression this feature
  exists for); nested values and sensitive values are excluded.
- `internal/classify`: `Result.Attributes` extracts from the matched set only;
  nil-drop and sort-unique; empty when the rule has no `emit_attributes`;
  multi-value dedupe across several matched changes; nothing emitted when a rule
  with `emit_attributes` does not fire.
- `internal/config`: parse `emit_attributes` on both `preset` and `rule`.
- `cmd/tfstackplan`: end-to-end — an IAM stack with two members across two
  projects → `attributes.project` is the sorted unique pair; a `safe` stack
  omits the `attributes` key entirely (assert `omitempty`).
- Goldens/examples: update any example that exercises `--emit-classification-json`.

## Risks

- **Memory:** `RawChange.Raw` adds a small per-change map. Bounded to top-level
  scalars; negligible beside the plan JSON already in memory.
- **Stringification of numbers/bools:** acceptable for sidecar gating data;
  documented that all emitted values are strings.
- **Sensitivity:** the deliberate sensitive-skip means a sensitive `project`
  (unusual) would silently not emit. Correct default — the alternative is
  leaking secrets into a JSON artifact.

## Downstream consumer (motivation, not part of this change)

After this ships, the `infra` repo's `cloudbuild.tier-plan.yaml` can delete the
`target_projects` half of its `classify_stack` `jq` and read `attributes.project`
straight from the sidecar, iterating it in the PAM grant loop. That CI swap is a
separate change in that repo.
