# Source-aware links

**Date:** 2026-05-29
**Status:** Approved (design)

## Problem

Reviewers want to click from the report into the code: a resource → its
declaration at the PR's commit, a stack → its directory, and the report → the
Cloud Build run / PR / commit. The plan JSON carries **no source location**
(`terraform-json` exposes `address`/`type`/`name`/`module_address` but no file
or line), so links must be derived from the source tree the tool runs alongside,
plus run context passed in.

## Goal

Emit clickable links at three levels:

- **Header (global):** build / PR / commit links under the title.
- **Stack:** the stack heading links to its directory at the commit.
- **Resource:** the resource address links to its `resource` block's
  `file#Lline` at the commit — resolved automatically by reading the `.tf`
  source. Falls back to the stack link when unresolvable.

Deep-linking to the PR *diff hunk* is out of scope (GitHub can't be
URL-addressed to a line in the diff view); we link to the block at `{sha}`,
which is the practical equivalent.

## Design

### Config (HCL templates) + run values (flags)

URL **templates** live in `.tfstackplan.hcl` (repo policy); per-run **values**
come from flags.

```hcl
links {
  resource = "https://github.com/org/infra/blob/{sha}/{file}#L{line}"
  stack    = "https://github.com/org/infra/tree/{sha}/{stack_dir}"
  header {
    label = "Cloud Build #{build_id}"
    url   = "https://console.cloud.google.com/cloud-build/builds/{build_id}?project={project}"
  }
  header {
    label = "PR #{pr}"
    url   = "https://github.com/org/infra/pull/{pr}"
  }
  header {
    label = "{sha_short}"
    url   = "https://github.com/org/infra/commit/{sha}"
  }
}
```

(`header` is a repeatable block with `label`/`url` attributes — one attribute
per line, since HCL single-line blocks allow only a single argument.)

Placeholders resolve from two sources:

- **Tool-computed vars:** `{file}`, `{line}`, `{stack}`, `{stack_dir}`, `{type}`,
  `{name}`, `{address}`, `{module}`, and `{sha_short}` (first 7 of `{sha}` when
  present).
- **Run values:** repeatable `--link-var key=value` (e.g. `sha`, `build_id`,
  `project`, `pr`). Unknown keys are simply available to templates.

A template that references a var with no value renders **empty** → that link is
omitted. So a repo can declare all three; a run that only supplies `sha` gets
resource/stack links but no build/PR header entries.

### New inputs

- `manifest.StackRef.dir` (optional) — the stack's source directory; defaults to
  the directory of its `plan` file.
- `--repo-root DIR` (flag) — base for computing repo-relative `{file}` /
  `{stack_dir}`. Defaults to the current directory.

### Components

| Package | Responsibility |
|---------|----------------|
| `internal/source` (**new**) | Per stack, build an index `(module_address, type, name) → {file, line}` (repo-relative file). Parse the stack `dir`'s `.tf` with `hclsyntax`; read `.terraform/modules/modules.json` (if present) to map `module_address → dir` and parse those too. Skips/falls back for remote-cached or missing dirs. Built lazily, only when `links.resource` is set. |
| `internal/links` (**new**) | Pure templating: `Resolve(template string, vars map[string]string) string` — substitute `{key}`; return `""` if any referenced key is missing/empty. Knows nothing about HTTP or terraform. |
| `internal/config` | Parse the `links {}` block (`resource`, `stack`, `header []{label,url}`). |
| `internal/model` | `Report.HeaderLinks []Link{Label,URL}`, `Stack.URL string`, `Change.URL string` — populated at gather time; render stays presentation-only. |
| `cmd/tfstackplan` | Flags `--repo-root`, `--link-var` (repeatable); build the source index per stack; resolve header/stack/resource URLs and fill `model`. |
| `internal/render` | Header line of `· `-joined `[label](url)` under the title; wrap the stack name and resource address in `<a href="url">…</a>` when a URL is set. |

### `internal/source` resolution detail

For a stack with source `dir` (repo-relative via `--repo-root`):

1. Parse every `*.tf` in `dir` (non-recursive — Terraform modules are flat per
   dir); for each top-level `resource "T" "N"` block, record
   `DefRange()` → `{file: <repo-rel path>, line}` under key `("", T, N)` (root
   module).
2. If `dir/.terraform/modules/modules.json` exists, for each entry `{Key, Dir}`
   parse that `Dir`'s `*.tf` and record under key `(Key, T, N)`. `Dir` values
   that resolve outside `--repo-root` (remote-cached modules) are recorded as
   "no repo file" → resource falls back to the stack link.
3. Lookup: a plan resource with `module_address` `module.a.module.b` → key
   `a.b` (strip `module.` segments); root → `""`. Strip the `[idx]`/`["k"]`
   instance key. Miss → fall back.

Cost: `hclsyntax` parsing is pure-Go and fast; ~ms per stack, dominated by I/O.
Negligible beside `terraform plan`.

### Flow

```
load (manifest + config + flags)
  → for each stack: source.Index(dir, repoRoot)        [only if links.resource set]
  → gather: per resource resolve {file,line}+vars → Change.URL (or stack URL on miss);
            per stack → Stack.URL; once → Report.HeaderLinks
  → fit (unchanged)
  → render: header links line; <a> around stack name / resource address
```

Links are resolved before `fit` and are pure strings on the model; `fit`/byte
budget are unaffected (URLs add bytes, counted normally).

## Testing

- `links`: `Resolve` substitutes vars, omits on missing key, leaves literals.
- `source`: fixture stack dir with root + a local module (+ a fake
  `modules.json`) → index maps root and module resources to the right
  file:line; `count`/`for_each` index stripped; remote/missing dir → no entry;
  a resource not found → miss.
- `config`: parse the `links {}` block (resource/stack/header), including a
  header entry list.
- `render`: stack name and resource address wrapped in `<a>` when URL set;
  header links line present; no `<a>` when unset.
- `cmd`: end-to-end with a fixture stack + `--repo-root` + `--link-var`s →
  output contains the expected blob/tree/commit URLs; a resource in a remote
  module falls back to the stack URL.
- examples: a new/updated golden showing linked output (or assert via the e2e
  test; goldens with absolute URLs are fine since vars are fixed in the test).

## Risks

- **Path relativization** across the stack dir and module dirs (from
  `modules.json`, whose `Dir` is relative to the stack dir) is the fiddly part;
  unit-tested in `internal/source`.
- **`.terraform/modules/modules.json` absent** (stack never `init`ed) → only
  root + the parse of `dir` works; module resources fall back. Acceptable.
- **HCL we can't parse** (syntax error in a `.tf`) → that file is skipped, its
  resources fall back; never fail the whole run for a link.
- **`{file}` for a resource defined across overrides** (`_override.tf`) → we
  take the first declaration found; documented, rare.
