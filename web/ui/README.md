# tfstackplan ui — the SPA

The SolidJS + TypeScript + Vite frontend of the central UI service
(`tfstackplan ui`). Built output is embedded into the Go binary at
`internal/ui/dist` — the repo commits only a placeholder there; CI/release
runs this build and overwrites it before `go build`, so `go build` never
needs node.

## Development

```sh
# 1. run a ui backend (any config with a ui {} block; no oauth block needed —
#    but then there is no login, so mint no session: use the vite proxy only
#    for /api testing against a session-less setup, or configure a dev oauth
#    client (fh-dev-svc) and log in for real).
tfstackplan ui --config dev.hcl --addr :8081

# 2. run the SPA with HMR; /api, /auth and /healthz proxy to :8081
yarn install
yarn dev
```

`yarn typecheck`, `yarn test` (vitest), `yarn build` (production bundle to
`dist/` — never commit it).

## Types

Payload types come from the tier contract: `yarn gen:types` regenerates
`src/api/tier-schema.d.ts` from `../../api/openapi.yaml` (committed; CI
verifies it is in sync). The UI backend proxies tier responses verbatim, so
those types are the wire truth.
