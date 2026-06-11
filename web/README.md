# web/ — UI asset toolchain (dev-only)

Compiles `input.css` (Tailwind v4 + DaisyUI) into the committed
`../internal/server/assets/app.css`, which the Go binary `go:embed`s. **The
committed CSS is what ships** — the Go build and CI never run node.

## Regenerate

```sh
yarn install      # once (uses yarn 1.22.22 via asdf; see .tool-versions)
yarn build        # writes ../internal/server/assets/app.css
```

Tailwind scans `../internal/server/templates/**/*.gohtml` and
`../internal/server/*.go` for class names, so any class you use must appear
literally there. Commit both `yarn.lock` and the regenerated `app.css`.
