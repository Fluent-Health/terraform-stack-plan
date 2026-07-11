import { defineConfig } from "vitest/config";
import solid from "vite-plugin-solid";
import tailwindcss from "@tailwindcss/vite";

// Local dev: `yarn dev` serves the SPA with HMR and proxies API + auth to a
// locally running `tfstackplan ui` backend (default :8081). Production build
// output is embedded into the Go binary (internal/ui/dist) by CI/release.
export default defineConfig({
  plugins: [solid(), tailwindcss()],
  server: {
    proxy: {
      "/api": "http://localhost:8081",
      "/auth": "http://localhost:8081",
      "/healthz": "http://localhost:8081",
    },
  },
  build: {
    sourcemap: true,
  },
  test: {
    environment: "node",
  },
});
