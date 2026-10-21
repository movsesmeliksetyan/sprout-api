// Package schema installs the job queue's tables. It is apart from package
// jobs so the migration runner and test databases can register it without
// importing every job.
package schema

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/rivermigrate"
)

// Version is the goose version of the job queue's tables. A SQL migration
// must not reuse it.
const Version = 16

// riverVersion is river's own schema version that Version installs. A
// river upgrade that brings a later one needs a new goose migration.
const riverVersion = 8

// Migration installs river's tables at Version and removes them going
// down. It runs outside goose's transaction: one of river's steps adds an
// enum value that a later step uses, which Postgres refuses in one
// transaction, so river applies each step in a transaction of its own.
func Migration() *goose.Migration {
	return goose.NewGoMigration(Version,
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error { return migrate(ctx, db, rivermigrate.DirectionUp) }},
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error { return migrate(ctx, db, rivermigrate.DirectionDown) }},
	)
}

func migrate(ctx context.Context, db *sql.DB, direction rivermigrate.Direction) error {
	migrator, err := rivermigrate.New(riverdatabasesql.New(db), nil)
	if err != nil {
		return fmt.Errorf("jobs: migrator: %w", err)
	}
	opts := &rivermigrate.MigrateOpts{TargetVersion: riverVersion}
	if direction == rivermigrate.DirectionDown {
		opts.TargetVersion = -1 // every river migration
	}
	if _, err := migrator.Migrate(ctx, direction, opts); err != nil {
		return fmt.Errorf("jobs: migrate %s: %w", direction, err)
	}
	return nil
}
