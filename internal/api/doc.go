// Package api holds the generated OpenAPI artifacts for the /api surface:
// request/response models (bound to internal/events via x-go-type — the
// events package stays the one canonical protocol definition), the std-http
// server router (ServerInterface + HandlerFromMux), and the typed Go client.
//
// The hand-written contract is api/openapi.yaml at the repo root; the
// generated code is committed and CI verifies it is in sync. Wire-level
// compatibility is pinned by internal/server/testdata/wire.
package api

//go:generate go tool oapi-codegen --config=config.yaml ../../api/openapi.yaml
