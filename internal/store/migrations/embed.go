// Package migrations embeds the goose SQL migrations for the server store.
package migrations

import "embed"

// FS holds the embedded *.sql goose migrations, applied by store.Open.
//
//go:embed *.sql
var FS embed.FS
