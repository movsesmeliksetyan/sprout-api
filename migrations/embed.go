// Package migrations holds the goose SQL migrations, embedded so the binary
// can apply them without the source tree.
package migrations

import "embed"

// FS contains every migration file.
//
//go:embed *.sql
var FS embed.FS
