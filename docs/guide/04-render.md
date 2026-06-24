# 04 — `render`

> A plan.json is what Terraform knows. A rendered comment is what a reviewer
> can use. The gap between them is the whole point.

`render` is a **pure, offline function**: give it a directory of
`plan.json` files — one per stack — and it emits a single Markdown
comment. No `terraform` binary, no GitHub token, no network. It reads
files; it writes Markdown. Your CI posts the result.

That purity is load-bearing. It means `render` is trivially reproducible,
runs in any CI without extra credentials, and can be adopted in an
afternoon without touching anything else. [The mental model chapter](02-mental-model.md)
goes into why that property is worth protecting; here we focus on what
the output looks like and how to read it.

## The comment, top to bottom

The first line of every rendered comment is an invisible HTML marker:

```html
<!-- tfstackplan:nonprod -->
```

This is what CI uses to find and replace the existing comment on the PR —
one upsert per environment, not a growing thread. You picked the marker
text when you ran `--marker tfstackplan:nonprod`. GitHub hides it; CI
depends on it.

After the marker comes the **summary table**: every changed stack in one
scannable row, with add/change/destroy/replace counts and (if
classification is on) category badges.

```markdown
### Terraform plan — nonprod  (8 stacks changed)

| Stack                      | Add | Change | Destroy | Replace | Categories |
| ---                        | --: |    --: |     --: |     --: | ---        |
| data/warehouse             |   0 |      0 |       6 |       0 | 💣 destructive |
| networking/shared-vpc      |   0 |      5 |       0 |       2 | 💣 destructive |
| platform/nonprod           |   0 |      4 |       0 |       0 | 🔐 iam     |
| service-projects/app-dev   |   4 |      3 |       0 |       0 | ✅ safe    |
```

Columns are only shown when they are non-zero across the run — a plan
with no replaces shows three count columns, not four. State operations
(moves, imports, forgets) get no columns; their counts append as text to
the stack's name cell (`platform/prod · 1 move, 2 imports`). The
Categories column appears only when classification is enabled.

Below the table comes the **per-stack drill-down**: one collapsible
section per stack, in alphabetical order.

## How to read a stack section

Each stack opens with a `<details>` whose heading is a folder icon and
the stack's bold name:

```
📁 platform/nonprod · 🔐 iam · 4 change
```

That heading reads like a section title — deliberately. It is visually
distinct from the resource rows nested inside it. The stack's body is
wrapped in a blockquote bar so GitHub draws a visible left margin; every
resource row lives inside that bar, making the stack → resource hierarchy
unambiguous at a glance.

**Every resource is a uniform `<details>` row.** Line 1 is an emoji
action glyph glued to the resource address with a non-breaking space, so
a long path can't orphan the icon when it wraps. The descriptor —
`N changed`, `replace`, and so on — hangs on an indented line below.

The action glyphs:

| Glyph | Meaning |
| ----- | ------- |
| ➕ | create |
| ➖ | delete |
| ✏️ | update |
| 🔁 | replace (destroy + create) |
| ↪️ | moved (address renamed in state) |
| 📥 | imported |
| ⏏️ | removed from state (forgotten) |

## Size-based folding

A resource row is **open by default when its rendered body is small**
(≤ ~10 lines) and **collapsed** when big. The rule is the same for
creates, deletes, updates — the tool measures, then decides. You can
override the default with `--details open` or `--details closed` if you
prefer all rows always in one state.

One thing worth knowing: GitHub's 65,536-byte comment cap counts the raw
Markdown *source*, including the content of collapsed `<details>`. Folding
helps reviewers scroll less; it does not save bytes. The byte budget is
managed by `fit`, a separate mechanism described below.

## Aligned scalar diffs

Inside an update row, scalars render as aligned `~ path = old → new`
lines. The `=` signs are column-aligned, so scanning a block of changes
reads like a table:

```diff
~ labels.env  = "dev" → "nonprod"
~ role         = "roles/viewer" → "roles/editor"
~ retention_days = 7 → 30
```

Nested maps keep their structure via dotted paths — so an added label
renders as `+ labels.team = "platform"` rather than a raw JSON blob. The
diff-body markers stay ASCII `+`/`-`/`~` so GitHub's diff view colours
them correctly.

## Structured values

When an attribute holds a JSON string, a YAML string, or a native HCL
map or list, the differ renders it as a **contextual diff** — not a raw
string comparison. The value is canonically re-formatted (sorted keys,
stable indent) before diffing, and the output shows 2 lines of context
around changed lines:

```diff
~ labels (yaml):
  env: nonprod
 +team: platform
```

The diff is tagged with the detected kind — `(json)`, `(yaml)` — so you
know what you are reading. Small structured diffs stay inline inside the
resource row; large ones collapse the row regardless of the 10-line rule
(the body size is the deciding factor either way).

Sensitivity is respected: when Terraform marks specific *leaves* of a
structured value as sensitive, only those leaves are redacted —
`(sensitive value)` appears where the secret lives, not across the entire
block, so the non-sensitive fields beside it remain readable.

## State-operation rows

Moves, imports, and forgets surface as resource rows with their own glyphs
and descriptors, but they carry no summary-table columns:

- **moved** (↪️): `addr / moved from previous.addr` — address change only,
  no apply-time write.
- **imported** (📥): `addr / imported · id=<code>id</code>` — the id is
  monospaced on the descriptor line.
- **removed from state** (⏏️): `addr / forgotten · N attrs` — the
  `removed {}` block with `lifecycle { destroy = false }`.

State operations never contribute a classification category (no
apply-time provider write, so no elevated permission needed). A resource
that is *both* moved and updated classifies on the underlying update.

## The byte budget

If the rendered comment would exceed GitHub's 65,536-byte cap, `fit`
degrades it deterministically — largest diff first, down the ladder from
full detail → summary line → hidden. The summary table and classification
are never reduced. If even an all-minimal report still overflows, the tool
emits a one-line aggregate and exits non-zero so CI can surface it. The
degradation is stable across re-runs of the same plan, which means CI
comment upserts do not churn when nothing changed.

The default budget is 60,000 bytes; `--max-bytes N` overrides it, and
`--max-bytes 0` disables the cap entirely.

## Classification, briefly

With a `.tfstackplan.hcl` in your repo, `render` classifies each stack
against your rules — `🔐 iam`, `💣 destructive`, and whatever else you
define — and shows the results in the summary table. Classification is
**fully optional**: with no config file the tool degrades gracefully and
emits no Categories column. The full story is in [05 — Classification →](05-classification.md).

## See it in action

A small diff snippet only goes so far. For a realistic sense of what the
rendered output looks like across many stacks — including over-budget
degradation and state-operation rows — the checked-in examples are the
real thing (regenerated and byte-checked by the test suite):

- [`examples/big-plan.md`](../../examples/big-plan.md) — 58 changes
  across 8 stacks, full detail.
- [`examples/over-budget-degraded.md`](../../examples/over-budget-degraded.md) —
  large diffs collapsed to summaries.
- [`examples/state-ops.md`](../../examples/state-ops.md) — moved,
  imported, and forgotten resources, plus contextual JSON/YAML diffs.

For the full flag list — `--marker`, `--details`, `--max-bytes`,
`--state-moves`, `--emit-classification-json`, links, and more — see
[`../reference/cli.md`](../reference/cli.md).

---

Next: [05 — Classification →](05-classification.md)
