# Image assets

Where the docs' visual assets live, what each is for, and how to (re)produce
them so they stay consistent over time.

## Inventory

| Asset | Status | Where it's embedded | How it's made |
|-------|--------|---------------------|---------------|
| `../architecture/00-four-faces.svg` | ✅ done | [guide/02](../guide/02-mental-model.md), [architecture](../architecture/) | D2 — `d2 00-four-faces.d2 00-four-faces.svg` |
| `hero-reviewer.jpg` | ✅ done | README hero | Nano-Banana / Gemini (prompt below); downscaled to ~1600px JPG |
| `aside-this-is-fine.jpg` | ✅ done | [guide/07 (serve)](../guide/07-serve.md) | Nano-Banana / Gemini (prompt below); downscaled to ~1400px JPG |
| `aside-the-adults.jpg` | ✅ done | [guide/01 (the gaps)](../guide/01-the-gaps.md) | Nano-Banana / Gemini (prompt below); downscaled to ~1400px JPG |
| `serve-dag.png` | ✅ done | README + [guide/07](../guide/07-serve.md) | `make screenshots` (automated chromedp capture) |
| `serve-gate.png` | ✅ done | [guide/07](../guide/07-serve.md) | `make screenshots` (automated chromedp capture) |
| `run.gif` | ⏳ to capture | [guide/06](../guide/06-run.md) | screen / asciinema cast of a `run` (see below) |

> Until an asset exists, **do not** add its `![…](…)` embed to a doc — a
> reference to a missing file renders as a broken image. Add the embed in the
> same change that adds the file.

## Humorous images — generation prompts

Tone: editorial-cartoon / wry New-Yorker-ish, clean, not slapstick. Landscape,
~16:9, readable at small size. Paste into Nano-Banana / Gemini image generation;
save the result to the filename in the table above.

### `hero-reviewer.png` — the quiet reviewer (README hero)

> A wide editorial illustration, muted modern palette. A software engineer sits
> alone at a desk at dusk, lit only by a monitor, holding a mug of long-cold
> coffee with a tea-bag tag hanging out. Their glasses reflect a GitHub-style
> pull-request page showing a huge "127 files changed" banner. Their expression
> is a thousand-yard stare — calm on the surface, quietly defeated underneath.
> Clean lines, soft shadows, a little negative space at the top for a caption.
> No text rendered in the image.

Caption to place under it in the README: *"He's reviewing the networking stack."*

### `aside-this-is-fine.png` — unattended apply (guide/07, serve)

> An editorial cartoon riff on the "this is fine" dog meme without copying it: a
> small dog in a hard hat sits calmly at a terminal in a server room as one
> monitor line glows ominously: `Destroying... google_sql_database_instance.prod`.
> A faint warm glow (not a literal fire) creeps in from the edges. Wry, deadpan,
> clean vector-ish style. No real meme characters. No text other than the
> terminal line.

### `aside-the-adults.png` — the adult in the room (guide/01, the gaps)

> An editorial illustration of a meeting room. Three figures around a table look
> pleased with themselves, each with a small desk-sign labelled "Terramate",
> "GitHub Actions", and "terraform apply". A fourth figure labelled
> "tfstackplan" stands at a whiteboard that reads: "who approved the prod
> destroy?" Dry corporate-comedy tone, clean modern illustration. Labels may be
> rendered as small desk-signs; keep the whiteboard line legible.

## Screenshots — capturing the live `serve` UI

Screenshots are automatically captured using ChromeDP via the `screenshots` target in the Makefile:

```bash
make screenshots
```

This runs the automated end-to-end tests in `e2e/screenshots_test.go` that boot the server with a seeded demo scenario and capture the relevant pages.

If you need to capture or adjust them manually, build the current binary and run `serve` locally against a seeded scenario, then screenshot the browser. Keep captures at a consistent width (~1400px) and trim to the relevant panel.

```bash
# Start serve in demo mode
go run ./cmd/tfstackplan serve --demo --addr :8080
```

- `serve-dag.png` — the dependency-DAG view mid-run, with a mix of
  done / in-flight / blocked stacks.
- `serve-gate.png` — an environment with a pending approval gate (a destructive
  or IAM change awaiting a human).

Re-capture when the UI changes materially; note the version in the commit.

## Run cast — `run.gif`

A short (≤ ~20 s) screen recording or [asciinema](https://asciinema.org/) cast of
`tfstackplan run` driving a small fixture through plan → gate → apply, showing
the in-process render and the lifecycle reporting. Export to GIF, keep it under a
few MB. The `cmd/tfstackplan/testdata/*fixture` trees are good inputs.
