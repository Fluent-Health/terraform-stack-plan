#!/usr/bin/env sh
# Regenerate the committed Tailwind+DaisyUI stylesheet that the Go binary embeds.
# The committed internal/server/assets/app.css is the source of truth for the
# binary; rerun this whenever template classes change. Requires yarn + node
# (see .tool-versions); nothing in the Go build runs this.
set -eu
cd "$(dirname "$0")"
OUT="../internal/server/assets/app.css"
mkdir -p "$(dirname "$OUT")"
npx --yes @tailwindcss/cli \
  -i input.css -o "$OUT" \
  --content '../internal/server/templates/**/*.gohtml' \
  --content '../internal/server/*.go' \
  --minify
echo "wrote $OUT"
