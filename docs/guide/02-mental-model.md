# 02 — The mental model

> One binary, four faces, one flow. Learn the flow and the faces fall out of it.

If you remember one thing about `tfstackplan`, remember this: it is **one flow**,
and each "face" of the CLI is an entry point into part of it. Data flows one way
— a plan comes in, and a reviewable, governable decision comes out.

![tfstackplan — four faces, four gaps](../architecture/00-four-faces.svg)

<details><summary>About this diagram</summary>

Source: [`../architecture/00-four-faces.d2`](../architecture/00-four-faces.d2).
Regenerate with `d2 00-four-faces.d2 00-four-faces.svg`.

</details>

## The four faces are the four gaps

Each face is the part of the flow that closes one of [the gaps](01-the-gaps.md):

| Face | Gap | What it is |
|------|-----|------------|
| [`render`](04-render.md) | Review | The pure, offline renderer: many `plan.json` → one comment. No `terraform`, no posting. |
| [`run`](06-run.md) | Observability | The CI driver: wraps `terramate script run`, executes plan/apply/verify, reports the lifecycle. |
| [`serve`](07-serve.md) | Governance | The control plane: approval gates, live DAG UI, per-environment check runs, streamed logs. |
| [`state`](08-state.md) | Management | Declarative cross-stack state moves, applied as part of the normal apply. |

## The most important property: `render` stands alone

`render` is a **pure function of its inputs**. Point it at a directory of
`plan.json` files and it emits Markdown. It never shells out to `terraform`, it
never calls GitHub, it keeps no state. You can adopt it this afternoon, in any
CI, without buying into anything else — and many people use only `render`,
forever, happily.

The other three faces *layer a control plane on top*. Crucially, they don't take
over your Terraform: the actual `plan`/`apply` keeps running in **your** CI,
under **your** identities. `serve` observes and gates; it doesn't hold your
credentials and run Terraform for you. That boundary is deliberate — it's what
lets you add governance without handing a long-lived service the keys to your
infrastructure.

## How the control plane thinks (one paragraph)

You don't need this to *use* the tool, but it explains the shape. `serve` is
**event-sourced**: commands produce events, an append-only log is the source of
truth, and everything you see — gate state, the DAG, check runs — is a
projection folded from that log. The core decision logic is a pure decider
(`Decide → Evolve → React`) with no I/O; the imperative shell does the I/O around
it. If that sounds interesting, the [architecture diagrams](../architecture/)
and [`DESIGN.md`](../DESIGN.md) go deep. If it doesn't, ignore it — the faces are
all you need.

## Where to go from here

You now have the whole map. The fastest way to feel it is the
[quickstart](03-quickstart.md): one directory of plans, one comment, sixty
seconds.

---

Next: [03 — Quickstart →](03-quickstart.md)
