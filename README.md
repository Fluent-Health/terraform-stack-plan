# tfstackplan

Render many Terraform `plan.json` files — one per stack, as produced by a
Terramate / Terragrunt / multi-root-module monorepo — into a **single,
reviewer-friendly markdown report** for a PR comment.

It's the multi-stack rollup that single-plan renderers like
[`tfplan2md`][tfplan2md] don't cover: a tier-level summary table plus a
collapsed per-stack drill-down, with optional classification to flag stacks
that need extra review (IAM, destructive changes, …).

[tfplan2md]: https://github.com/oocx/tfplan2md

---

## What it looks like

A run over eight stacks with classification enabled, rendered the way GitHub
shows it in a PR comment: a scannable summary table, then a per-stack
drill-down. Inside a stack, **each resource is its own row within an indented
blockquote bar** — so it's always clear which stack/resource you're reading.
Small changes are shown expanded; big ones collapse to a row you click to open.
(The stacks below are shown expanded for illustration.)

<!-- tfstackplan:nonprod -->

### Terraform plan — nonprod  (8 stacks changed)

| Stack | Add | Change | Destroy | Replace | Class |
| --- | ---: | ---: | ---: | ---: | --- |
| platform/nonprod | 0 | 4 | 0 | 0 | 🔐 iam |
| service-projects/app-dev | 4 | 3 | 0 | 0 | ✅ safe |
| data/warehouse | 0 | 0 | 6 | 0 | 💣 destructive |
| networking/shared-vpc | 0 | 5 | 0 | 2 | 💣 destructive |
| observability/grafana | 5 | 6 | 0 | 0 | ✅ safe |

<details open><summary>platform/nonprod · 🔐 iam · 4 change</summary>

> <details open><summary>~ google_project_iam_member.data_engineers · 1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>
> <details open><summary>~ google_storage_bucket.tfstate · 2 changed</summary>
>
> ```diff
> ~ retention_days = 7 → 30
> ~ labels (yaml):
>  env: nonprod
> +team: platform
> ```
>
> </details>

</details>

Scalars render as aligned `~ path = old → new` leaves; structured attributes
(here `labels`) render as a contextual diff. Creates and deletes look the same —
one open row per resource showing its attributes:

<details open><summary>data/warehouse · 💣 destructive · 6 destroy</summary>

> <details open><summary>- google_bigquery_dataset.legacy_events · 2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_events"
> ```
>
> </details>

</details>

Structured values (JSON/YAML strings and native HCL maps/lists) render as a
**contextual diff** — canonically re-formatted, 2 lines of context, changed
lines as `-`/`+`, tagged with the kind. Small ones stay inline (open); big ones
**collapse** to a closed row:

<details open><summary>observability/grafana · ✅ safe · structured fields (excerpt)</summary>

> <details open><summary>~ kubernetes_manifest.ingress · 1 changed</summary>
>
> ```diff
> ~ manifest (yaml):
>  spec:
> -    key_00: old
> -    key_01: old
> +    key_00: new
> +    key_01: new
>      key_02: old
>      key_03: old
> ```
>
> </details>
> <details><summary>~ kubernetes_manifest.configmap · 1 changed</summary>
>
> ```diff
> ~ manifest (yaml):
>  spec:
> -    key_00: old
> -    key_01: old
> +    key_00: new
> +    key_01: new
>   … (many paths changed → folds to a closed row)
> ```
>
> </details>

</details>

The first line of the real output is an HTML-comment marker
(`<!-- tfstackplan:nonprod -->`, invisible above) that CI uses to upsert one
comment per tier.

Key render behaviours:
- **Every resource is a uniform `<details>` row** inside the stack's blockquote bar, giving a clear stack → resource hierarchy.
- **Size-based folding:** a row is open when its body is small (≤ ~10 lines), collapsed when big — the same rule for creates, deletes, and updates.
- **Aligned changes:** `~ path = old → new`, with `=` aligned and nested maps keeping their name via dotted paths (`+ labels.team = "platform"`).
- **Structured values** (JSON/YAML strings and native HCL maps/lists) render as a **contextual diff** — the value canonically re-formatted, 2 lines of context, changed lines as `-`/`+`, tagged with its kind (`~ policy (json):`). Small diffs stay inline; big ones collapse the row.
- **State operations** surface as rows too: moved (`↪ addr · moved from …`), imported (`⤓ addr · imported (id=…)`), and removed-from-state (`⊘ addr · forgotten`). These have no summary-table columns; their counts append to the stack's row text.
- `--details open|auto` overrides the per-row default; zero-only columns are dropped; without a classification policy the `Class` column disappears.

### More examples

Larger reports stay under GitHub's 65,536-byte comment cap by degrading the
biggest diffs first, then dropping detail. These files are real tool output
(regenerated and byte-checked by `go test ./cmd/tfstackplan`):

- [`examples/big-plan.md`](examples/big-plan.md) — 58 changes across 8 stacks,
  full detail, fits the default 60 KB budget.
- [`examples/over-budget-degraded.md`](examples/over-budget-degraded.md) —
  tighter budget: large diffs collapse to one-line summaries
  (`~ data · text · 120 lines · 240 changed (hidden to fit size limit)`), small
  diffs kept.
- [`examples/over-budget-summary-only.md`](examples/over-budget-summary-only.md) —
  tighter still: all `<details>` dropped, summary table + a notice retained.
- [`examples/over-budget-minimal.md`](examples/over-budget-minimal.md) — past
  every simplification and still over budget: a one-line aggregate is emitted
  and the tool exits non-zero so CI can surface it.
- [`examples/state-ops.md`](examples/state-ops.md) — **moved** (`↪`),
  **imported** (`⤓`), and **removed-from-state / forget** (`⊘`) resources; and
  **contextual diffs** for nested JSON, YAML, and native HCL blocks (2 lines of
  context, `-`/`+` changes), in small (inline) and big (folded) form.

---

## Install

```bash
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest
```

Or grab a prebuilt binary from the
[Releases](https://github.com/Fluent-Health/terraform-stack-plan/releases) page,
or build from source: `go build -o tfstackplan ./cmd/tfstackplan`.

---

## Usage

Each stack contributes one `plan.json` (from `terraform show -json plan.bin`).
List them inline:

```bash
tfstackplan \
  --stack platform/nonprod:./out/platform-nonprod/plan.json \
  --stack service-projects/app-dev:./out/app-dev/plan.json \
  --title  "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

Or describe the run in a manifest (YAML or JSON):

```yaml
# plan.yaml
title: "Terraform plan — nonprod"
marker: "tfstackplan:nonprod"
stacks:
  - name: platform/nonprod
    plan: ./out/platform-nonprod/plan.json
  - name: service-projects/app-dev
    plan: ./out/app-dev/plan.json
```

```bash
tfstackplan --manifest plan.yaml --output report.md
```

The manifest carries **only** the run's `title`, `marker`, and stack list.
Classification and diff handling are repo policy and live in a separate HCL file
(below). With no policy file, the tool runs with zero config — counts and diffs,
no `Class` column.

---

## Classification (optional)

Classification is **computed by the tool**, not supplied per stack: each stack's
plan is matched against rules in an HCL policy file (`--config`, or
auto-discovered `.tfstackplan.hcl` in the working directory). The first rule that
fires sets the class; unmatched stacks get the `default`.

```hcl
# .tfstackplan.hcl  — repo policy, checked into git
classification {
  default {
    name = "safe"
    icon = "✅"
  }

  preset "iam" {            # built-in matcher; you only pick the icon
    icon = "🔐"
  }

  rule "destructive" {      # custom matcher
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}
```

`preset` and `rule` blocks evaluate **top-to-bottom in source order**, first hit
wins — put the most important classes first.

**Presets** ship a maintained matcher so you don't hand-write regexes; only the
icon is configurable:

| Preset | Matches | Default icon |
|--------|---------|--------------|
| `iam`  | IAM resources on GCP (`*_iam_{policy,binding,member,audit_config}`), AWS (`aws_iam_*`), Azure (`azurerm_role_{assignment,definition}`). Any action, so an in-place policy update still flags as `iam`. | `🔐` |

**Custom rules** take the class name from the block label, plus a small matcher:

| Field                   | Meaning                                                          | Default    |
|-------------------------|------------------------------------------------------------------|------------|
| `icon`                  | Glyph prepended to the class name                               | none       |
| `resource_type_pattern` | Regex matched against each change's `type`                      | `.*` (any) |
| `actions`               | A change matches only if ALL listed actions appear in it        | any action |
| `min_count`             | Minimum matching changes for the rule to fire                   | 1          |

`default` can also be written as a bare string (`default = "safe"`) when you
don't want an icon.

### Sidecar JSON for CI gating

Because the class is computed, `--emit-classification-json` hands CI the result
as data — gate on it instead of re-parsing the markdown:

```bash
tfstackplan --manifest plan.yaml --config .tfstackplan.hcl \
            --output report.md --emit-classification-json classes.json
```

```json
{
  "platform/nonprod":         { "class": "iam",  "icon": "🔐" },
  "service-projects/app-dev": { "class": "safe", "icon": "✅" }
}
```

`icon` is `null` when the class has none; the flag is a no-op without
classification.

---

## Diff configuration (optional)

The same HCL file can tune per-attribute diffs via a `diff {}` block. The
built-in renderer sniffs structured values (JSON / YAML / base64) and picks a
sensible diff style; use a `rule` to force one when detection misfires.

```hcl
diff {
  detect              = true   # auto-detect JSON/YAML/base64 (default: true)
  max_attribute_lines = 200    # optional skimmability ceiling; unset = fit decides

  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"   # exact name or glob
    differ                = "yaml"
  }
}
```

---

## CLI reference

```
tfstackplan [--manifest FILE | --stack NAME:PATH ...]
            [--title TEXT] [--marker TEXT]
            [--config FILE]                 # HCL policy; default: auto-discover .tfstackplan.hcl
            [--max-bytes N]                 # default 60000; 0 disables
            [--details auto|open|closed]    # default closed (auto = open iff one stack changed)
            [--emit-classification-json FILE]
            [--output FILE | -]             # default '-' (stdout)
            [--version]
```

`--manifest` and `--stack` are mutually exclusive. `--title` / `--marker` on the
command line override the manifest. With no `--config` and no `.tfstackplan.hcl`
present, classification is off and diffs use defaults.

---

## Out of scope

- **Posting** to GitHub / GitLab / Bitbucket — the tool writes markdown; your CI
  posts it. Keeps it a pure, offline-testable renderer.
- **Running `terraform plan`** — inputs are pre-existing `plan.json` files.
- **Static-analysis rollup** (Checkov, Trivy, SARIF) — possible later.

See [`docs/DESIGN.md`](docs/DESIGN.md) for architecture and design rationale.

## Related tools

- [`tfplan2md`][tfplan2md] — single-plan markdown renderer; inspiration for the
  per-stack diff style.
- [Atlantis](https://www.runatlantis.io/) — PR-comment-driven Terraform CI;
  renders per-project, tied to its own workflow.
- [`terraform-plan-summary`](https://github.com/dineshba/terraform-plan-summary) —
  single-plan summary table; no multi-plan support.

---

## Contributing

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). For security
issues, follow [SECURITY.md](SECURITY.md) rather than opening a public issue.

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE).

---

Built and maintained by [Fluent Health](https://github.com/Fluent-Health).
