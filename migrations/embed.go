// Package migrations holds the goose SQL migrations, embedded so the binary
// can apply them without the source tree. Some versions are Go migrations
// instead: 00012 and 00015 (merchant keys, internal/backfill) and 00016 (the
// job queue's tables, internal/jobs/schema). A SQL file must not reuse one.
package migrations

import "embed"

// FS contains every migration file.
//
//go:embed *.sql
var FS embed.FS
