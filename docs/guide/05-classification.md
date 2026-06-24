# 05 — Classification

> A reviewer who can see "💣 destructive" in the summary table doesn't have
> to read every diff to know something needs a second look.

Classification is how `render` turns a column of change counts into a column
of *intent* — a badge like `🔐 iam` or `💣 destructive` that tells reviewers
at a glance which stacks matter most, and tells CI gates exactly which
approvals to request.

It is **entirely optional**. With no config file, `render` skips it silently
and the `Categories` column simply disappears from the summary table. Nothing
breaks; you just get counts.

To turn it on, drop a `.tfstackplan.hcl` at your repo root. `render`
auto-discovers it by walking up from the working directory to the first
ancestor that contains `.git`, so a command run from a stack subdirectory
finds the repo-root policy with no `--config` flag.

---

## The three building blocks

Classification is built from three pieces: a **default**, **presets**, and
**rules**. They live inside a single `classification {}` block and are
evaluated in declaration order.

```hcl
# .tfstackplan.hcl
classification {
  default { name = "safe", icon = "✅" }

  preset "iam" {
    icon            = "🔐"
    emit_attributes = ["project"]
  }

  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}
```

### The default

`default` is the display fallback: it appears in the comment when a stack
matches no rule. You can use the block form (with an icon) or the one-liner:

```hcl
default = "safe"   # shorthand; no icon
```

The default is display-only — it never appears in the sidecar JSON
(`--emit-classification-json`) or in the run-level summary, and it plays no
part in CI gating.

### Presets

A `preset` is a named bundle of built-in rules. You don't write the matcher;
you just give the preset an icon and optionally an `emit_attributes` list.
The one built-in preset is **`iam`**, which matches IAM resource types across
providers:

- GCP / Google Beta: `*_iam_{policy,binding,member,audit_config}`
- AWS: `aws_iam_*`
- Azure: `azurerm_role_{assignment,definition}`

It leaves `actions` unset by design, so an in-place IAM-policy update still
flags — not just creates.

`emit_attributes` names the attributes the preset extracts from matched
resources and includes in the sidecar JSON. This is the data a CI gate
consumes to request the right approvals (e.g. which GCP projects need a PAM
grant). See [`../reference/configuration.md`](../reference/configuration.md)
for the full `derive {}` mechanism that recovers an attribute from a
neighbouring scalar when the change itself doesn't carry it.

### Rules

A `rule` is a custom matcher. Every field is optional:

| Field | Meaning | Default |
|-------|---------|---------|
| `icon` | Glyph prepended to the name | none |
| `resource_type_pattern` | Regex matched against the resource type | `.*` (any) |
| `actions` | Rule fires only if **all** listed actions appear | any action |
| `min_count` | Minimum matching changes required | 1 |

`actions` is the list to watch. `["delete"]` matches only deletes; omitting
it matches anything. A change qualifies if every action you list appears in
its `actions[]` (so `["delete", "create"]` means replace, since a replace
carries both).

Rules with no matcher fields are catch-alls — they fire on every stack.

### What never classifies

Classification considers only changes that mutate the real resource:
`add`, `change`, `destroy`, and `replace`. Pure state operations — `move`,
`import`, and `forget` — never contribute a category. A resource that is
simultaneously moved *and* updated classifies on the underlying `update`.

### How categories surface

The result for each stack is the **set of all matching categories**, in
declaration order. That order determines the badge display order; there is no
first-hit-wins — every matching rule contributes. A stack that matches both
`iam` and `destructive` shows both badges.

Categories appear in two places:

1. **The `Categories` column** in the summary table — `🔐 iam · 💣 destructive`.
2. **The per-stack header** — `📁 data/warehouse · 💣 destructive · 6 destroy`.

The full schema is in [`../reference/configuration.md`](../reference/configuration.md).

---

## The byte budget

GitHub applies a 65,536-byte limit to PR comment source, and collapsed
`<details>` elements still count in full. The only way to stay under the cap
is to actually summarize or omit content.

`render` does this automatically with `--max-bytes` (default `60000`, leaving
a margin for any wrapper text your CI adds). Set it to `0` to disable the
budget entirely.

The fit pass starts every attribute at its richest variant and, while the
document exceeds the budget, advances the **currently largest** attribute one
rung toward a lossier form. The degradation ladder for structured values
(JSON/YAML/HCL blocks) is:

1. **Structural** — a contextual unified diff of the canonically formatted
   value (the preferred form).
2. **Summary** — a one-line descriptor: `~ content · yaml · 412 lines · 18
   changed (hidden to fit size limit)`.
3. **Hidden** — the attribute is omitted from the diff body.

Plain text follows a similar ladder; base64/binary starts at Summary. The
sort order within each pass is deterministic (bytes descending, then stack
name, then resource address), so re-runs of an unchanged plan produce
byte-identical output — CI upserts don't churn.

The summary table and classification badges are **never** reduced by this
loop.

### When even all-minimal isn't enough

If every attribute is already Hidden and the document still exceeds the
budget, the renderer cascades at the report level:

1. **Summary-only** — all per-stack `<details>` bodies are dropped; the full
   summary table is kept; a notice is appended.
2. **Minimal summary** — the table is replaced with a one-line aggregate
   (`N stacks · A adds · C changes · D destroys · K flagged iam`), plus a
   budget notice.
3. **Best-effort floor** — the marker line plus the minimal aggregate are
   emitted regardless; if even that exceeds a pathologically small budget, the
   tool emits it anyway and exits non-zero so CI can surface the problem.

The marker comment (`<!-- tfstackplan:nonprod -->`) is always line 1 and
survives at every rung.

Example reports at each rung are in the repo:

- [`examples/big-plan.md`](../../examples/big-plan.md) — full detail, under
  the default 60 KB budget.
- [`examples/over-budget-degraded.md`](../../examples/over-budget-degraded.md)
  — large diffs collapsed to summaries.
- [`examples/over-budget-summary-only.md`](../../examples/over-budget-summary-only.md)
  — all per-stack detail dropped.
- [`examples/over-budget-minimal.md`](../../examples/over-budget-minimal.md)
  — one-line aggregate, tool exits non-zero.

---

## Diff config and links

The same `.tfstackplan.hcl` accepts two optional extras alongside
`classification {}`.

### `diff {}`

Tunes per-attribute rendering. The most common use is forcing a specific
differ when auto-detection misfires:

```hcl
diff {
  detect = true
  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"
    differ                = "yaml"
  }
}
```

`max_attribute_lines` is an optional readability ceiling; when set, the
differ won't emit a rich variant larger than that line count, regardless of
remaining budget. Unset (the default) means full detail up to the global fit
pass.

### `links {}`

Adds URL templates to headers, stack names, and individual resources. The
most useful link is from a resource address to its `.tf` declaration in the
commit:

```bash
tfstackplan render --plans-dir out/ \
  --repo-root . \
  --link-var sha=$GIT_SHA
```

`--repo-root` resolves file paths for link generation (default `.`).
`--link-var key=value` injects template variables (repeatable). Both are
optional; absent means no links are emitted.

The full template syntax and available variables are in
[`../reference/configuration.md`](../reference/configuration.md). The full
flag list is in [`../reference/cli.md`](../reference/cli.md).

---

Next: [06 — `run` →](06-run.md)
