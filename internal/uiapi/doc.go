// Package uiapi holds the generated OpenAPI artifacts for the central UI's
// backend API (api/ui.openapi.yaml): the std-http router and models. The
// tier-proxied schemas are x-go-type-bound to internal/api, so the UI contract
// re-exposes the tier contract's shapes rather than duplicating them. The SPA
// is the intended consumer; its TypeScript types are generated from the same
// document. Generated code is committed; CI verifies it is in sync.
package uiapi

//go:generate go tool oapi-codegen --config=config.yaml ../../api/ui.openapi.yaml
