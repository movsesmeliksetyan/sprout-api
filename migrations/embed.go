// Package migrations holds the goose SQL migrations, embedded so the binary
// can apply them without the source tree. Some versions are Go migrations
// instead, kept in internal/backfill: 00012 (merchant keys). A SQL file must
// not reuse one.
package migrations

import "embed"

// FS contains every migration file.
//
//go:embed *.sql
var FS embed.FS
