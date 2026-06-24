# tfstackplan — the guide

> Terramate runs your stacks. This is everything that has to happen *around*
> that run for a team to trust it.

`tfstackplan` is the layer that lets a Terraform monorepo scale to a team inside
GitHub's PR workflow. It makes the many plans a monorepo emits per PR
**reviewable**, the apply **governable**, the run **observable**, and cross-stack
refactors **manageable** — four gaps, [four faces](guide/02-mental-model.md).

These docs read in order. If you've got 60 seconds, jump to the
[quickstart](guide/03-quickstart.md). If you want to understand the shape of the
thing first, start at the top.

## The guide, in order

| # | Chapter | What you'll get |
|---|---------|-----------------|
| 01 | [The gaps](guide/01-the-gaps.md) | *Why* this exists — what a monorepo × GitHub PR review leaves broken |
| 02 | [The mental model](guide/02-mental-model.md) | The one idea: four faces, four gaps, one flow |
| 03 | [Quickstart](guide/03-quickstart.md) | `render` standalone → your first PR comment, in a minute |
| 04 | [`render`](guide/04-render.md) | The reviewer's comment: folding, aligned diffs, structured values, state ops |
| 05 | [Classification](guide/05-classification.md) | Presets + rules, categories, and the byte budget |
| 06 | [`run`](guide/06-run.md) | The CI driver — plan / apply / verify, reporting the lifecycle |
| 07 | [`serve`](guide/07-serve.md) | The control plane — gates, the live DAG UI, per-stack logs, check runs |
| 08 | [`state`](guide/08-state.md) | Declarative cross-stack Terraform state moves |
| 09 | [CI integration](guide/09-ci-integration.md) | Wiring it into GitHub Actions and Terramate |

## Reference

The dry, exact material — every flag, the full config schema, environment
variables, install & deploy — lives under [`reference/`](reference/). Start with
[Configuration](reference/configuration.md) when you're ready to write a
`.tfstackplan.hcl`.

## How it's built

- [Architecture](architecture/) — five diagrams: the faces, the hexagon, the
  event-sourced control plane, the gate lifecycle, the CI run sequence.
- [`DESIGN.md`](DESIGN.md) — the living design doc: decisions, rationale, and the
  current known limitations.

## Worked examples

[`examples/`](../examples/) holds real rendered outputs — a big plan, the byte
budget kicking in, state operations — each reproducible from the renderer.

---

Next: [01 — The gaps →](guide/01-the-gaps.md)
