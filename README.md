# tfstackplan

> Your monorepo just opened **8 Terraform plans** on this one PR.
> Your reviewer has gone very quiet.

<p align="center">
  <img src="docs/images/hero-reviewer.jpg" alt="An engineer at dusk, cold coffee in hand, a '127 files changed' sign glowing in the window" width="760">
  <br><sub><i>He's reviewing the networking stack.</i></sub>
</p>

`tfstackplan` turns the N `plan.json` files a monorepo spits out per PR into a
single comment a human can actually read — then, if you're brave, gates the
apply behind a button someone has to consciously press.

Terramate makes a Terraform monorepo *runnable*. `tfstackplan` makes it
**reviewable, governable, and safe** inside GitHub's PR workflow. It closes the
four gaps a raw `terramate run` in CI leaves wide open — and it has **four
faces**, one per gap:

| Face | The gap it closes | In one line |
| --- | --- | --- |
| [**`render`**](docs/guide/04-render.md) | **Review** | N unreadable `plan.json` → one scannable PR comment |
| [**`serve`**](docs/guide/07-serve.md) | **Governance** | no approval story → live gates + privilege-backed apply |
| [**`run`**](docs/guide/06-run.md) | **Observability** | CI logs are a black hole → live DAG UI, per-stack logs, check runs |
| [**`state`**](docs/guide/08-state.md) | **Management** | cross-stack refactors are scary → declarative state moves |

> Terramate runs your stacks. GitHub shrugs and posts the logs. `terraform
> apply` does exactly what you told it to, which is the problem. `tfstackplan`
> is the adult in the room.

`render` is **fully standalone** — point it at a directory of plans and you get
a comment. You never have to touch `run` / `serve` / `state`. The other three
layer a control plane on top, while Terraform keeps executing in *your* CI under
*your* identities.

New here? Start with **[the guide](docs/index.md)** — it reads in order, from
*why this exists* to *every flag there is*.

---

## What it looks like

A run over eight stacks with classification on, rendered the way GitHub shows it
in a PR comment: a scannable summary table, then a per-stack drill-down. Each
resource is its own row inside an indented blockquote bar, so it's always clear
which stack and resource you're reading. Small changes show expanded; big ones
collapse to a row you click to open.

<!-- tfstackplan:nonprod -->

### Terraform plan — nonprod  (8 stacks changed)

| Stack | Add | Change | Destroy | Replace | Categories |
| --- | ---: | ---: | ---: | ---: | --- |
| platform/nonprod | 0 | 4 | 0 | 0 | 🔐 iam |
| service-projects/app-dev | 4 | 3 | 0 | 0 | ✅ safe |
| data/warehouse | 0 | 0 | 6 | 0 | 💣 destructive |
| networking/shared-vpc | 0 | 5 | 0 | 2 | 💣 destructive |
| observability/grafana | 5 | 6 | 0 | 0 | ✅ safe |

<details open><summary>📁&nbsp;<b>platform/nonprod</b> · 🔐 iam · 4 change</summary>

>
> <details open><summary>✏️&nbsp;google_project_iam_member.data_engineers<br>&nbsp;&nbsp;&nbsp;&nbsp;1 changed</summary>
>
> ```diff
> ~ role = "roles/viewer" → "roles/editor"
> ```
>
> </details>

</details>

<details open><summary>📁&nbsp;<b>data/warehouse</b> · 💣 destructive · 6 destroy</summary>

>
> <details open><summary>➖&nbsp;google_bigquery_dataset.legacy_events<br>&nbsp;&nbsp;&nbsp;&nbsp;2 attrs</summary>
>
> ```diff
> - location = "us-central1"
> - name     = "legacy_events"
> ```
>
> </details>

</details>

That whole block is **live Markdown, not a screenshot** — it renders natively on
GitHub and you can paste it anywhere. The first line of the real output is an
invisible HTML-comment marker (`<!-- tfstackplan:nonprod -->`) that CI uses to
upsert one comment per environment. The full anatomy — folding rules, aligned
diffs, structured-value rendering, state-op rows — lives in
**[the render chapter](docs/guide/04-render.md)**.

More worked outputs (big plans, the byte budget kicking in, state ops) are in
**[`examples/`](examples/)**.

---

## 60-second quickstart

`render` is offline and standalone — no `terraform`, no posting, no config
required. Give it a directory of plans laid out to mirror your stack tree.

```bash
# 1. install
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest

# 2. for each stack, capture a plan as JSON next to where it lives
terraform plan -out plan.bin && terraform show -json plan.bin > out/<stack>/tfplan.json

# 3. render every stack under out/ into one comment
tfstackplan render --plans-dir out/ \
  --title  "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

`report.md` is the comment you saw above. With no config, classification is off
and the tool degrades gracefully — add a `.tfstackplan.hcl` when you want
categories, links, and budgets. Then have your CI post `report.md`; `render`
writes Markdown, your CI does the posting.

The full path from here — classification, then the control plane — is laid out
in **[the guide](docs/index.md)**.

---

## The four faces

**[`render`](docs/guide/04-render.md) — the review gap.** A pure, offline
renderer: many `plan.json` files in, one reviewer-friendly Markdown comment out,
with optional classification. It never runs `terraform` and never posts. Use it
entirely on its own.

**[`run`](docs/guide/06-run.md) — the observability gap.** The CI driver. It
wraps your `terramate script run`, detects the changed stacks, runs
plan/apply/verify, renders + classifies in-process, and reports the execution
lifecycle to the control plane — so a CI run stops being a wall of interleaved
logs.

**[`serve`](docs/guide/07-serve.md) — the governance gap.** The control plane: a
live dependency-DAG UI, approval gates, one GitHub check run per environment,
and SSE-tailed per-stack logs.

> `terraform apply` across 8 stacks, straight from CI, with no one watching, is
> a perfectly reasonable thing to do. `serve` is for the other 100% of cases.

**[`state`](docs/guide/08-state.md) — the management gap.** Declarative
cross-stack Terraform state moves, applied as part of the normal apply — so
refactoring resources between stacks stops being a hand-run, fingers-crossed
operation.

---

## Where do I go now?

The docs read as a sequence — **[start at the index](docs/index.md)** — but if
you know what you want:

- **Why does this exist?** → [The gaps](docs/guide/01-the-gaps.md) ·
  [Mental model](docs/guide/02-mental-model.md)
- **I just want a comment.** → [Quickstart](docs/guide/03-quickstart.md) ·
  [`render`](docs/guide/04-render.md) ·
  [Classification](docs/guide/05-classification.md)
- **I want a control plane.** → [`run`](docs/guide/06-run.md) ·
  [`serve`](docs/guide/07-serve.md) ·
  [CI integration](docs/guide/09-ci-integration.md)
- **I'm moving resources between stacks.** → [`state`](docs/guide/08-state.md)
- **Just give me every flag.** → [Reference](docs/reference/index.md) ·
  [CLI](docs/reference/cli.md) · [Configuration](docs/reference/configuration.md)
- **How is it built?** → [Architecture](docs/architecture/) ·
  [`DESIGN.md`](docs/DESIGN.md)

---

## Install

```bash
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest
# or
go build -o tfstackplan ./cmd/tfstackplan
```

Prebuilt binaries (linux/darwin · amd64/arm64) are on the
[Releases](https://github.com/Fluent-Health/terraform-stack-plan/releases) page,
which also ships a multi-arch, distroless **Cloud Run container** (entrypoint
`serve`). The binary is fully static — pure-Go SQLite, no cgo — and embeds its
assets, so the image needs no runtime files.

This repo is also **its own [asdf](https://asdf-vm.com) plugin** (hook scripts in
[`bin/`](./bin), no separate plugin repo):

```bash
asdf plugin add tfstackplan https://github.com/Fluent-Health/terraform-stack-plan.git
asdf install tfstackplan latest      # or a pinned version
asdf set tfstackplan latest          # writes .tool-versions
```

Full install detail — toolchain notes, container, deployment — is in
[Reference → Install & deploy](docs/reference/install-and-deploy.md).

---

## Agentic Workflows, Extensions & Workspace Skills

`tfstackplan` publishes a comprehensive, cross-runtime **Workspace Agent Skill** directly in this repository under `skills/tfstackplan/SKILL.md`. This skill governs the entire lifecycle and CLI surface of `tfstackplan` (lint, plan, state refactoring, watch status, claims, and applies), instructing future coding agents on how to operate this codebase with maximum safety and TDD discipline.

### Loading in Claude Code (Marketplace & Plugins)

We package `tfstackplan` as a standard Claude Code plugin. You can load this plugin directly from GitHub or subscribe to our marketplace feed:

1. **Install directly as a Claude Plugin:**
   ```bash
   /plugin add https://github.com/Fluent-Health/terraform-stack-plan
   ```
2. **Add as a Marketplace Catalog:**
   ```bash
   /plugin marketplace add https://raw.githubusercontent.com/Fluent-Health/terraform-stack-plan/main/.claude-plugin/marketplace.json
   ```
*Claude Code will automatically detect `.claude-plugin/plugin.json` and load the configured `./skills` on demand when your session context matches the skill's frontmatter description, executing these instructions natively via the Claude `Skill` tool.*

### Loading in Gemini CLI

You can install this repository's agent skill and extensions globally or link them locally during development:

1. **Install globally as a Gemini Extension:**
   ```bash
   gemini extensions install https://github.com/Fluent-Health/terraform-stack-plan
   ```
2. **Link the skill locally:**
   ```bash
   gemini skills link ./skills/tfstackplan
   ```
*Once loaded, Gemini CLI will automatically trigger and follow this skill whenever you ask the model to validate, plan, or restructure Terraform state.*

### Manual / Cross-Runtime Loading

Both Claude Code and Gemini CLI support the open [Agent Skills](https://agentskills.io) standard, enabling cross-runtime interoperability:

1. **Create the global skill directory:**
   ```bash
   mkdir -p ~/.agents/skills/tfstackplan
   ```
2. **Symlink or copy the skill file:**
   ```bash
   ln -s "$(pwd)/skills/tfstackplan/SKILL.md" ~/.agents/skills/tfstackplan/SKILL.md
   ```

---

## When *not* to use this

Honesty up front saves everyone a wasted afternoon:

- **One root module, one plan per PR.** If `terraform plan` already produces a
  single readable diff, you don't need a *multi*-plan renderer. Reach for
  [`tfplan2md`](https://github.com/oocx/tfplan2md) or
  [`terraform-plan-summary`](https://github.com/dineshba/terraform-plan-summary).
- **You want the tool to run `terraform` for you.** `render` doesn't; its inputs
  are pre-existing `plan.json` files. (`run` *does* drive Terraform via
  Terramate — that's a different face.)
- **You want it to post comments for you.** `render` writes Markdown; your CI
  posts it. (`serve` posts its own check runs, but that's the control plane, not
  the renderer.)
- **You need static-analysis rollups** (Checkov / Trivy / SARIF). Not today;
  possibly later.

---

## Related tools

Yes, it's another Terraform tool. We're as surprised as you are. The difference
is the **multi-plan** part — taking the many plans a monorepo emits per PR and
making them one reviewable, gate-able, observable thing:

- [`tfplan2md`](https://github.com/oocx/tfplan2md) — single-plan Markdown
  renderer; the inspiration for the per-stack diff style.
- [Atlantis](https://www.runatlantis.io/) — PR-comment-driven Terraform CI;
  renders per-project, tied to its own workflow.
- [`terraform-plan-summary`](https://github.com/dineshba/terraform-plan-summary)
  — single-plan summary table; no multi-plan support.

---

## Contributing

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). For security
issues, follow [SECURITY.md](SECURITY.md) rather than opening a public issue.

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE).

---

Built and maintained by [Fluent Health](https://github.com/Fluent-Health).
