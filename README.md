# tfstackplan

A tool for rendering multi-stack Terraform plans into a single, reviewer-friendly
markdown report. Designed to fill the gap between [`tfplan2md`][tfplan2md] (great
at one plan) and the reality of monorepo / multi-stack setups (many plans per PR).

[tfplan2md]: https://github.com/oocx/tfplan2md

---

## Context — the problem

Monorepos using Terramate, Terragrunt, Atlantis-style multi-project setups, or
any other "N independent root modules per repo" pattern produce **N plan.json
files per PR**, not one. The natural way to surface this in a PR is:

- One comment per environment / tier
- A tier-level summary so reviewers see scope at a glance
- Per-stack drill-down for the actual diffs

`tfplan2md` is excellent at the per-stack rendering but is built around the
one-plan-in / one-document-out model. There's no native concept of "tier,"
"stack," or "merged report."

---

## Gaps in using tfplan2md alone for multi-stack PRs

1. **Single-plan input.** Each `tfplan2md` invocation takes exactly one plan
   JSON. No merge, no multi-input mode.

2. **No cross-plan grouping.** Module grouping is intra-plan (Terraform modules
   within one root). There's no notion of "stack" or "environment" above the
   plan level.

3. **Templating is going away.** Originally `tfplan2md` used the Scriban template
   engine — `.sbn` files were extensible. Per their [ADR-010][adr-010], the
   project is moving to pure-C# rendering because "user-customizable templates
   are no longer a requirement." Custom templates are not a sustainable path.

4. **No tier-level summary primitive.** A multi-stack PR needs a top-level
   summary table (which stacks changed, how many adds/changes/destroys, which
   ones touch sensitive resources like IAM). That has to be hand-rolled outside
   `tfplan2md`.

5. **No classification primitive.** Many gated CI flows want to mark certain
   stacks as needing extra review (IAM changes, prod-blast-radius changes,
   schema migrations). `tfplan2md` has no notion of categorising a plan as
   `iam` vs `safe` vs anything else — that has to be reinvented in each
   pipeline.

6. **No comment-size handling.** GitHub PR comments are capped at 65,536 bytes.
   A multi-stack report can blow past that easily on large refactors. The
   per-stack rendering doesn't know about a global budget.

7. **Provider focus is Azure-heavy.** Specialized renderers exist for Azure
   NSG/Firewall rules, Azure AD, Azure DevOps. GCP and AWS get the generic
   default template — fine, but no provider-specific polish for them.

[adr-010]: https://github.com/oocx/tfplan2md/blob/main/docs/adr-010-scriban-removal-evaluation.md

---

## Current workaround (what we do today, manually)

For a single PR comment per tier, in CI we:

1. Run `terraform show -json plan.bin > plan.json` per changed stack.
2. Hand-roll the summary table by reading each `plan.json` (count
   `resource_changes` grouped by `actions[]`).
3. Invoke `tfplan2md` once per stack against its `plan.json`.
4. Concatenate everything into one markdown document:
   - Summary table at the top.
   - One collapsed `<details>` per stack containing that stack's
     `tfplan2md` output.
5. Upsert as a marker-keyed PR comment (`<!-- tf-plan:<tier> -->`).

Works, but it's ~150 lines of bash, fragile, and reinvents pieces for
each repo that needs it.

---

## What a dedicated tool should do

### Target output shape

```markdown
<!-- tfstackplan:nonprod -->
### Terraform plan — nonprod  (3 stacks changed)

| Stack                          | Adds | Changes | Destroys | Class  |
|--------------------------------|-----:|--------:|---------:|--------|
| platform/nonprod                  |    0 |       1 |        0 | ⚠️ iam |
| service-projects/app-dev    |    2 |       0 |        0 | safe   |
| service-projects/app-test   |    0 |       0 |        0 | safe   |

<details><summary>platform/nonprod  ·  iam  ·  1 change</summary>

[per-stack rendered diff]

</details>

<details><summary>service-projects/app-dev  ·  safe  ·  2 adds</summary>

[per-stack rendered diff]

</details>
```

### Inputs

The tool takes a set of `(stack-name, plan-json-path)` pairs plus optional
metadata per stack:

```bash
tfstackplan \
  --stack platform/nonprod:./out/platform-nonprod/plan.json:iam \
  --stack service-projects/app-dev:./out/app-dev/plan.json:safe \
  --title "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

Or via a manifest file (JSON / YAML) for ergonomics:

```yaml
title: "Terraform plan — nonprod"
marker: "tfstackplan:nonprod"
stacks:
  - name: platform/nonprod
    plan: ./out/platform-nonprod/plan.json
  - name: service-projects/app-dev
    plan: ./out/app-dev/plan.json
```

If a `classification:` block is present, the tool computes the `class` per
stack from the plan JSON (see next section). Otherwise stacks default to
unclassified — adds/changes/destroys only, no class column or icon.

A caller can also pin `class:` per-stack to override auto-classification.

### Behaviour

- **Counts:** parse each plan.json and count `resource_changes` by primary
  action (`create`, `update`, `delete`, `replace`, `no-op`). Drive the summary
  table.
- **Per-stack diff:** render each plan via either:
  - A built-in renderer (sufficient for GCP/AWS, generic enough for Azure), or
  - Shelling out to `tfplan2md` for compatibility (each invocation is one
    plan in / one block out — exactly tfplan2md's sweet spot).
- **Optional classification** (see next section): when the manifest defines
  `classification:` rules, the tool computes a class per stack and shows it
  in the summary table + `<details>` heading. When absent, no class column —
  the tool degrades gracefully for simple use cases.
- **Collapsed by default.** All `<details>` start closed. Reviewer expands what
  they care about.
- **Comment-budget aware.** Optional `--max-bytes` flag; if total exceeds it,
  truncate the most verbose per-stack section with a "[…truncated, N lines]"
  marker. Keep the summary table intact.
- **Marker-keyed output.** First line is the HTML comment so the CI side can
  use it for upsert detection.

---

## Classification (optional)

A `classification:` block in the manifest turns on auto-classification. Each
stack's plan JSON is scanned against ordered rules; the first matching rule
sets the class. Stacks with no match fall back to `default:`.

```yaml
classification:
  default: safe                  # class used when no rule matches
  rules:
    - name: iam
      icon: "⚠️"
      resource_type_pattern: '^(google|google-beta)_[a-z_]*_iam_(policy|binding|member|audit_config)$'
    - name: destructive
      icon: "💣"
      actions: ["delete"]
      min_count: 1
    - name: schema-migration
      icon: "🗄️"
      resource_type_pattern: '^google_sql_database$|^google_bigquery_dataset$'
      actions: ["update", "replace"]

stacks:
  - name: platform/nonprod
    plan: ./out/platform-nonprod/plan.json
  - name: service-projects/app-dev
    plan: ./out/app-dev/plan.json
```

**Rule matcher** stays deliberately small — no DSL, no boolean expressions:

| Field                   | Meaning                                                            | Default          |
|-------------------------|--------------------------------------------------------------------|------------------|
| `name`                  | The class name, shown in the summary table                         | required         |
| `icon`                  | Glyph prepended to the name (e.g. `⚠️`)                            | none             |
| `resource_type_pattern` | Regex matched against each change's `type` (e.g. `google_compute_instance`) | `.*` (any)       |
| `actions`               | List of action strings; rule matches if ALL listed appear in change's `actions[]` | any action       |
| `min_count`             | Minimum number of matching changes for the rule to apply           | 1                |

Rules are evaluated top-to-bottom; first hit wins. A rule with no matcher
fields is a catch-all.

### Sidecar JSON output

When classification is enabled, the tool can emit a structured artefact for
CI to consume programmatically:

```bash
tfstackplan --manifest plan.yaml --output report.md \
              --emit-classification-json classes.json
```

```json
{
  "platform/nonprod":             { "class": "iam",  "icon": "⚠️" },
  "service-projects/app-dev": { "class": "safe", "icon": null }
}
```

This is what lets a CI pipeline drive gating logic (e.g. "if any class is
`iam`, request a PAM grant before merge") off the same source of truth that
renders the PR comment. No re-grepping the markdown, no duplicate classifier
code in bash.

### Why optional

The simplest team — one stack per PR, no privileged-resource gating —
should be able to use this tool with zero config beyond `stacks:` and get a
clean tier-summary + diffs report. Classification is for teams running
gated pipelines; it shouldn't be a tax on everyone else.

### CLI surface (rough)

```
tfstackplan [--manifest FILE | --stack NAME:PATH[:CLASS] ...]
              [--title TEXT]
              [--marker TEXT]
              [--render auto|builtin|tfplan2md]
              [--max-bytes N]
              [--details auto|open|closed]
              [--emit-classification-json FILE]
              [--output FILE | -]
```

`--render=tfplan2md` shells out to the `tfplan2md` binary per stack.
`--render=builtin` uses an in-process renderer (faster, no dependency).
`--render=auto` picks `tfplan2md` if it's on PATH, else builtin.

`--emit-classification-json` writes the computed class per stack as JSON
(see [§Classification](#classification-optional)). No-op when classification
isn't configured.

---

## Design decisions to make before coding

1. **Language.** Go and Rust both compile to single static binaries — easy to
   ship via Homebrew + Docker the way `tfplan2md` does. Python is faster to
   prototype but ships less cleanly. **Lean: Go** (closest to the Terraform
   ecosystem; `terraform-json` is a Go module, easy parsing of plan JSON).

2. **Built-in renderer vs. always-shell-out.** Always-shell-out is the
   simplest v1 (every renderer responsibility delegated to `tfplan2md`). A
   built-in renderer is cleaner long-term but more work. **Lean: ship v1 with
   shell-out only; add built-in renderer in v2** if/when `tfplan2md`'s upstream
   direction diverges from what we need.

3. **Manifest format.** YAML, JSON, or HCL? **Lean: YAML** — humans-write +
   common in CI configs. JSON falls out as a subset.

4. **Templating?** Don't repeat `tfplan2md`'s mistake of building a templating
   surface that nobody uses. **Lean: no user templates in v1.** Reconsider if
   real demand emerges. (Classification is a different shape — small
   declarative ruleset, not free-form templates — and is in scope.)

7. **Classification scope.** The matcher could grow into a DSL. **Lean: keep
   it three fields (`resource_type_pattern`, `actions`, `min_count`) and
   resist temptation to add booleans, custom functions, or nested rules.**
   If a team needs richer rules, they can pre-process the plan JSON and
   inject a `class:` override per stack.

5. **Multi-tier in one comment?** The current shape is one comment per tier.
   Should the tool know about tiers, or stay one-comment-per-invocation?
   **Lean: one-comment-per-invocation.** Caller invokes once per tier.
   Simpler tool, same outcome.

6. **Diff between runs?** Optional v2 feature: take a previous run's report
   and highlight what changed since. Useful but not v1.

---

## Out of scope

- Posting to GitHub / GitLab / Bitbucket. The tool writes markdown; the CI
  pipeline posts it. Reasoning: keeping the tool a pure renderer makes it
  reusable across platforms and easy to test offline.
- Running `terraform plan` itself. Inputs are pre-existing `plan.json` files.
- Replacing `tfplan2md`. v1 shells out to it; v2 may add a built-in renderer
  to remove the dependency, but `tfplan2md` remains a perfectly good per-plan
  renderer to delegate to.
- Static analysis integration (Checkov, Trivy, etc.). `tfplan2md` already does
  this per-plan via SARIF. If we want a tier-level rollup of findings, that's
  a v2+ thing.

---

## Related tools & prior art

- [`tfplan2md`][tfplan2md] — single-plan markdown renderer. Strong inspiration;
  this tool wraps it.
- [Atlantis](https://www.runatlantis.io/) — opinionated PR-comment-driven
  Terraform CI. Renders per-project; ties tightly to its own workflow.
- [Terramate Cloud](https://terramate.io/cloud) — paid SaaS, renders
  multi-stack drift / plan reports.
- [`terraform-plan-summary`](https://github.com/dineshba/terraform-plan-summary) —
  small CLI that prints a single-plan summary table; no multi-plan support.

---

## What this repo contains today

Just this README. The brief is the artifact.

---

## Usage

Build:

```bash
go build -o tfstackplan ./cmd/tfstackplan
```

Render from a manifest, with classification + sidecar JSON for CI gating:

```bash
tfstackplan \
  --manifest examples/manifest.yaml \
  --config   examples/.tfstackplan.hcl \
  --output   report.md \
  --emit-classification-json classes.json
```

Or list stacks inline (no manifest file):

```bash
tfstackplan \
  --stack platform/nonprod:./out/platform-nonprod/plan.json \
  --stack service-projects/app-dev:./out/app-dev/plan.json \
  --title  "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod
```

With no `--config` and no `.tfstackplan.hcl` in the working directory,
classification is off (no Class column) and diffs use sensible defaults — the
tool runs with zero config.

The first line of the output is the marker HTML comment
(`<!-- tfstackplan:nonprod -->`); a CI step uses it to upsert a single PR
comment per tier. `--max-bytes` (default 60000) keeps the document under
GitHub's 65,536-byte cap; see [`docs/DESIGN.md`](docs/DESIGN.md) for the
degradation cascade.

### Install

```bash
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest
```

Or download a prebuilt binary for your platform from the
[Releases](https://github.com/Fluent-Health/terraform-stack-plan/releases) page.

---

## Contributing

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). For security
issues, please follow [SECURITY.md](SECURITY.md) rather than opening a public issue.

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE).

---

Built and maintained by [Fluent Health](https://github.com/Fluent-Health).
