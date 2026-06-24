# 01 — The gaps

> A monorepo is a great way to manage fifty Terraform stacks and a terrible way
> to review them.

You moved your infrastructure into a monorepo because the alternative — fifty
repos, fifty pipelines, fifty places for drift to hide — was worse. You adopted
[Terramate](https://terramate.io/) so one `terramate script run` could plan and
apply across all of them, in dependency order, in CI. Good calls, both.

Then you opened a pull request that touched shared networking, and CI produced
**eight `terraform plan` outputs**, and you discovered that the tools stop right
where the hard part begins.

Here's the thing nobody warns you about: Terramate is, by design, **strictly
per-stack**. It runs a command in each stack; there is no reduce step, no
roll-up, no "here is everything this PR will do" view. Plan previews are a
per-stack feature. So the moment you have more than one stack, four things that
were trivial in a single-root-module repo quietly break.

## Gap 1 — Review

Eight plans is eight walls of `terraform` output. A reviewer has to open each
job, scroll the noise, hold "what changed across all of these" in their head,
and somehow notice that one of them is destroying a database. GitHub's PR review
was built for code diffs, not for N plans stapled together in N CI logs. There
is no single artifact a human can read. So nobody really reads them.

## Gap 2 — Governance

Once the plans are merged, `terraform apply` runs. Across eight stacks. Straight
from CI. Doing exactly what the plans said — including the destroy you didn't
catch in Gap 1. There's no approval surface that understands "this environment,
this set of stacks, this person clicked approve," no way to require a human in
front of a destructive change, no concept of *who* is allowed to apply *what*.
The apply either runs or it doesn't.

## Gap 3 — Observability

While the apply runs, where is it? Which stacks are done, which are in flight,
which failed, which are blocked waiting on a dependency? The answer lives in
interleaved CI logs that update when the job feels like it. A multi-stack apply
is a black box with a spinner.

## Gap 4 — Management

Eventually you'll want to move a resource from one stack to another — split a
stack, rename one, pull a bucket out of "platform" into its own thing. In a
monorepo that's a cross-stack `terraform state mv`, run by hand, against prod,
with your fingers crossed, outside the normal apply. There's no declarative,
reviewable way to say "these resources move here" as part of the change.

## What closes them

These four gaps are not four separate tools' problems. They're one tool's
problem — `tfstackplan` — and it closes them with [four faces](02-mental-model.md):

- **Review** → `render` turns the N plans into one comment.
- **Governance** → `serve` adds gates and privilege-backed apply.
- **Observability** → `run` + `serve` give you a live view of the run.
- **Management** → `state` makes cross-stack moves declarative.

Terramate keeps doing what it's good at — running your stacks. `tfstackplan` is
everything that has to happen *around* that run for a team to trust it.

---

Next: [02 — The mental model →](02-mental-model.md)
