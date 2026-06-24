# 03 — Quickstart

> No `terraform`, no posting, no config, no server. Just plans in, comment out.

The fastest way to understand `tfstackplan` is to watch `render` turn a pile of
plans into a single comment. `render` is offline and standalone, so this takes
about a minute and commits you to nothing.

## 1. Install

```bash
go install github.com/Fluent-Health/terraform-stack-plan/cmd/tfstackplan@latest
```

(Prebuilt binaries and an asdf plugin are in
[Reference → Install & deploy](../reference/install-and-deploy.md).)

## 2. Capture each stack's plan as JSON

`render` reads `terraform`'s machine-readable plan. For each stack, lay the JSON
out in a directory that **mirrors your stack tree** — the directory holding each
`tfplan.json` becomes the stack's name:

```bash
# in each stack:
terraform plan -out plan.bin
terraform show -json plan.bin > out/<stack>/tfplan.json
```

So `out/platform/nonprod/tfplan.json` renders as the stack `platform/nonprod`.
(In real CI, [`run`](06-run.md) does this capture for you across every changed
stack — but you don't need it to try `render`.)

## 3. Render every stack into one comment

```bash
tfstackplan render --plans-dir out/ \
  --title  "Terraform plan — nonprod" \
  --marker tfstackplan:nonprod \
  --output report.md
```

`report.md` is a single Markdown comment: a summary table across all stacks, then
a per-stack drill-down. Stacks render alphabetically; an empty or absent set of
plans renders a tidy "0 stacks changed" header. That's the whole contract —
`render` writes Markdown, and **your CI posts it** (the `--marker` is an invisible
HTML comment your CI uses to upsert one comment per environment).

## 4. (Optional) turn on classification

With no config, classification is off and the tool degrades gracefully. Drop a
`.tfstackplan.hcl` at your repo root to get categories like `🔐 iam` and
`💣 destructive`, a byte budget, and source links:

```hcl
classification {
  default { name = "safe", icon = "✅" }
  preset "iam" { icon = "🔐" }
  rule "destructive" {
    icon      = "💣"
    actions   = ["delete"]
    min_count = 1
  }
}
```

`render` auto-discovers `.tfstackplan.hcl` by walking up to the repo root, so a
command run from a stack subdirectory still finds it. The full schema is in
[Configuration](../reference/configuration.md); what the categories *do* is in
[Classification](05-classification.md).

## Where to next

- Understand the comment itself — folding, aligned diffs, structured values —
  in [`render`](04-render.md).
- Ready for gates, a live UI, and per-environment check runs? That's the control
  plane: [`run`](06-run.md) and [`serve`](07-serve.md).

---

Next: [04 — `render` →](04-render.md)
