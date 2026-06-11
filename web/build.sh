#!/usr/bin/env sh
# Regenerate the committed Tailwind+DaisyUI stylesheet that the Go binary embeds.
# Tailwind v4 discovers which classes to emit from the @source globs in input.css
# (the templates + server Go), so any class used must appear literally there.
# The committed internal/server/assets/app.css is the source of truth for the
# binary; rerun this whenever template classes change. Requires yarn + node
# (see .tool-versions); nothing in the Go build runs this.
set -eu
cd "$(dirname "$0")"
OUT="../internal/server/assets/app.css"
mkdir -p "$(dirname "$OUT")"
npx --yes @tailwindcss/cli -i input.css -o "$OUT" --minify
echo "wrote $OUT"
