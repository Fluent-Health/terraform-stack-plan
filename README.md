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

There are two kinds of input, kept deliberately separate:

1. **The per-run stack list** (the *what*) — which stacks changed and where
   their plan JSON lives. Passed via repeated `--stack NAME:PATH` flags…

   ```bash
   tfstackplan \
     --stack platform/nonprod:./out/platform-nonprod/plan.json \
     --stack service-projects/app-dev:./out/app-dev/plan.json \
     --title "Terraform plan — nonprod" \
     --marker tfstackplan:nonprod \
     --output report.md
   ```

   …or, for ergonomics, a manifest file (JSON / YAML):

   ```yaml
   title: "Terraform plan — nonprod"
   marker: "tfstackplan:nonprod"
   stacks:
     - name: platform/nonprod
       plan: ./out/platform-nonprod/plan.json
     - name: service-projects/app-dev
       plan: ./out/app-dev/plan.json
   ```

   The manifest carries *only* `title`, `marker`, and the `stacks` list. It
   has no per-stack class, icon, or other metadata — that is computed, not
   supplied (see below).

2. **The policy** (the *how*) — an HCL file (`.tfstackplan.hcl`, auto-discovered
   in the working directory or pointed at with `--config`) that declares
   classification rules and diff handling. This is repo-level config, checked
   in once, not part of any single run.

> **Classification is an output, not an input.** Earlier drafts let callers
> tag each stack with a `class:` in the manifest (or as a third `:CLASS` field
> on `--stack`). That is gone. The tool *derives* a class per stack by running
> the plan JSON against the policy's rules, then surfaces it in the Class
> column, the `<details>` heading, and the optional
> `--emit-classification-json` sidecar. If no policy file is present, stacks
> are unclassified — adds/changes/destroys only, no Class column or icon.

### Behaviour

- **Counts:** parse each plan.json and count `resource_changes` by primary
  action (`create`, `update`, `delete`, `replace`, `no-op`). Drive the summary
  table.
- **Per-stack diff:** render each plan with the built-in renderer — a
  self-contained differ that sniffs attribute values (JSON, YAML, base64,
  plain) and picks a sensible diff style per attribute. No external binary
  is required. (The original plan was to shell out to `tfplan2md`; that was
  dropped in favour of an in-process renderer — see Design decisions.)
- **Optional classification** (see next section): when a policy file defines
  classification rules, the tool computes a class per stack and shows it
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

Classification is configured in the **HCL policy file** (`.tfstackplan.hcl`),
*not* in the manifest. The presence of a `classification {}` block turns it on.
Each stack's plan JSON is scanned against the block's ordered rules; the first
rule that matches enough changes sets the class. Stacks with no match fall back
to the configured `default`.

```hcl
# .tfstackplan.hcl
classification {
  default {
    name = "safe"
    icon = "✅"
  }

  # A built-in preset: ships a ready-made matcher; you only choose the icon.
  preset "iam" {
    icon = "🔐"
  }

  # A hand-written rule.
  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}
```

`preset` and `rule` blocks are evaluated **top-to-bottom in source order**;
the first to fire wins, so put the most important classes first.

### Presets

A `preset "<name>" {}` block pulls in a built-in, maintained matcher so a repo
can classify common cases without hand-writing regexes. Only the `icon` is
configurable; the matcher itself is fixed.

| Preset | Matches | Default icon |
|--------|---------|--------------|
| `iam`  | IAM resources across GCP (`*_iam_{policy,binding,member,audit_config}`), AWS (`aws_iam_*`), and Azure (`azurerm_role_{assignment,definition}`). Any action — an in-place policy update still classifies as `iam`. | `🔐` |

Referencing an unknown preset is an error that lists the available names.

### Custom rules

A `rule "<name>" {}` block is a hand-written matcher. The class name comes from
the block label. The matcher stays deliberately small — no DSL, no boolean
expressions:

| Field                   | Meaning                                                                            | Default    |
|-------------------------|------------------------------------------------------------------------------------|------------|
| *(label)*               | The class name, shown in the summary table (e.g. `rule "destructive"`)             | required   |
| `icon`                  | Glyph prepended to the name (e.g. `💣`)                                            | none       |
| `resource_type_pattern` | Regex matched against each change's `type` (e.g. `google_compute_instance`)        | `.*` (any) |
| `actions`               | List of action strings; a change matches only if ALL listed actions appear in it   | any action |
| `min_count`             | Minimum number of matching changes for the rule to fire                            | 1          |

A rule with no matcher fields is a catch-all (every change matches).

### The `default` class

`default` sets the class for stacks no rule matched. Use the block form for an
icon, or the shorthand string form for name-only:

```hcl
classification {
  default = "safe"          # shorthand: name only, no icon
}
```

### Sidecar JSON output

Because the class is computed, CI often wants it as structured data rather than
re-parsing the markdown. With classification enabled, `--emit-classification-json`
writes the computed class per stack:

```bash
tfstackplan --manifest plan.yaml --config .tfstackplan.hcl \
              --output report.md \
              --emit-classification-json classes.json
```

```json
{
  "platform/nonprod":         { "class": "iam",  "icon": "🔐" },
  "service-projects/app-dev": { "class": "safe", "icon": "✅" }
}
```

`icon` is `null` when the matched class has no glyph. The flag is a no-op when
classification isn't configured.

This is what lets a CI pipeline drive gating logic (e.g. "if any class is
`iam`, request a PAM grant before merge") off the same source of truth that
renders the PR comment. No re-grepping the markdown, no duplicate classifier
code in bash.

### Why optional

The simplest team — one stack per PR, no privileged-resource gating —
should be able to use this tool with zero config (no policy file at all) and
get a clean tier-summary + diffs report. Classification is for teams running
gated pipelines; it shouldn't be a tax on everyone else.

---

## Diff configuration (optional)

The same HCL policy file can tune how per-attribute diffs are rendered, via a
`diff {}` block. Like classification, it's entirely optional — with no policy
file, sensible defaults apply.

```hcl
diff {
  detect              = true   # sniff JSON/YAML/base64 values (default: true)
  max_attribute_lines = 200    # optional skimmability ceiling; unset = global fit decides

  # Force a specific differ for a (resource type, attribute) pair.
  rule {
    resource_type_pattern = "^kubernetes_manifest$"
    attribute             = "manifest"
    differ                = "yaml"
  }
}
```

| Field                     | Meaning                                                                   | Default        |
|---------------------------|---------------------------------------------------------------------------|----------------|
| `detect`                  | Auto-sniff structured attribute values (JSON / YAML / base64)             | `true`         |
| `max_attribute_lines`     | Cap on lines rendered per attribute diff                                  | unset (fit decides) |
| `rule.resource_type_pattern` | Regex selecting which resource types the override applies to            | any            |
| `rule.attribute`          | Glob matched against the attribute name                                   | any            |
| `rule.differ`             | Differ to force for matching attributes (e.g. `yaml`)                     | auto           |

### CLI surface

```
tfstackplan [--manifest FILE | --stack NAME:PATH ...]
              [--title TEXT]
              [--marker TEXT]
              [--config FILE]                 # HCL policy; default: auto-discover .tfstackplan.hcl
              [--max-bytes N]                 # default 60000; 0 disables
              [--details auto|open|closed]    # default closed
              [--emit-classification-json FILE]
              [--output FILE | -]             # default '-' (stdout)
              [--version]
```

`--manifest` and `--stack` are mutually exclusive. `--title` and `--marker`
on the command line override values from the manifest.

`--config` points at the HCL policy file; if omitted, `.tfstackplan.hcl` in the
working directory is auto-discovered. With no policy file, classification is off
and diffs use defaults.

`--emit-classification-json` writes the computed class per stack as JSON
(see [§Classification](#classification-optional)). No-op when classification
isn't configured.

`--details` controls the `<details>` disclosure state: `closed` (default),
`open`, or `auto` (open only when exactly one stack changed).

---

## Design decisions (resolved)

1. **Language → Go.** Compiles to a single static binary — easy to ship via
   Homebrew + Docker the way `tfplan2md` does — and it's closest to the
   Terraform ecosystem (plan JSON parses cleanly from Go).

2. **Built-in renderer, not shell-out.** The original lean was to shell out to
   `tfplan2md` for per-stack rendering. We instead ship an **in-process
   renderer** (the `internal/differ` package): no external binary on PATH, and
   full control over the diff/fit cascade that keeps reports under GitHub's
   comment cap. There is no `--render` flag.

3. **Two config surfaces, by concern.** The per-run *stack list* is YAML/JSON
   (`--manifest`) — humans write it, it's common in CI. The repo-level
   *policy* (classification + diff rules) is **HCL** (`.tfstackplan.hcl`) —
   block-structured, labelled rules, and a natural fit for the Terraform
   audience. Keeping the two separate means the per-PR input stays tiny and the
   policy is checked in once.

4. **No user templates.** We don't repeat `tfplan2md`'s templating surface that
   nobody used. Classification is a different shape — a small declarative
   ruleset, not free-form templates.

5. **Classification matcher stays small.** Three fields
   (`resource_type_pattern`, `actions`, `min_count`) plus built-in presets. No
   booleans, custom functions, or nested rules. Teams needing richer logic can
   consume the `--emit-classification-json` sidecar and gate in their pipeline.

6. **One comment per invocation.** The tool doesn't know about "tiers"; the
   caller invokes it once per tier. Simpler tool, same outcome.

### Possible later work

- **Diff between runs.** Take a previous run's report and highlight what changed
  since. Useful, not yet built.

---

## Out of scope

- Posting to GitHub / GitLab / Bitbucket. The tool writes markdown; the CI
  pipeline posts it. Reasoning: keeping the tool a pure renderer makes it
  reusable across platforms and easy to test offline.
- Running `terraform plan` itself. Inputs are pre-existing `plan.json` files.
- Replacing `tfplan2md` head-on. It's strong inspiration and a perfectly good
  single-plan renderer; this tool solves the multi-stack rollup it doesn't.
  (We ship our own in-process renderer rather than delegating to it.)
- Static analysis integration (Checkov, Trivy, etc.). `tfplan2md` already does
  this per-plan via SARIF. If we want a tier-level rollup of findings, that's
  a v2+ thing.

---

## Related tools & prior art

- [`tfplan2md`][tfplan2md] — single-plan markdown renderer. Strong inspiration
  for the per-stack diff style; this tool covers the multi-stack rollup it
  doesn't.
- [Atlantis](https://www.runatlantis.io/) — opinionated PR-comment-driven
  Terraform CI. Renders per-project; ties tightly to its own workflow.
- [Terramate Cloud](https://terramate.io/cloud) — paid SaaS, renders
  multi-stack drift / plan reports.
- [`terraform-plan-summary`](https://github.com/dineshba/terraform-plan-summary) —
  small CLI that prints a single-plan summary table; no multi-plan support.

---

## What this repo contains today

A working Go implementation: the `tfstackplan` CLI (`cmd/tfstackplan`) plus the
internal packages it's built from — `manifest` (stack list), `config` (HCL
policy), `classify` + `presets` (classification), `plan` (plan-JSON parsing),
`differ` (per-attribute diffs), `fit` (comment-budget cascade), and `render`
(markdown output). See [`docs/DESIGN.md`](docs/DESIGN.md) for internals.

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
