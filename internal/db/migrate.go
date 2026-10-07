package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/pressly/goose/v3"

	"github.com/movsesmeliksetyan/sprout-api/migrations"
)

// Migration commands accepted by Migrate.
const (
	MigrateUp     = "up"
	MigrateDown   = "down"
	MigrateStatus = "status"
)

// ErrUnknownMigrateCommand is returned by Migrate for any other command.
var ErrUnknownMigrateCommand = errors.New("unknown migrate command")

// Migrate runs a goose command against the embedded migrations and reports
// what it did on out. Up applies everything pending, down rolls back the
// latest migration, status lists each migration's state.
func Migrate(ctx context.Context, databaseURL, command string, out io.Writer) error {
	switch command {
	case MigrateUp, MigrateDown, MigrateStatus:
	default:
		return fmt.Errorf("%w: %q", ErrUnknownMigrateCommand, command)
	}

	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		// The driver's message can quote the URL, which holds the password.
		return errors.New("db: database URL could not be parsed")
	}
	defer sqlDB.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("db: load migrations: %w", err)
	}

	switch command {
	case MigrateUp:
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("db: migrate up: %w", err)
		}
		if len(results) == 0 {
			fmt.Fprintln(out, "no migrations to apply")
		}
		for _, r := range results {
			fmt.Fprintf(out, "applied      %s (%s)\n", name(r.Source), r.Duration)
		}
	case MigrateDown:
		result, err := provider.Down(ctx)
		if errors.Is(err, goose.ErrNoNextVersion) {
			fmt.Fprintln(out, "no migrations to roll back")
			return nil
		}
		if err != nil {
			return fmt.Errorf("db: migrate down: %w", err)
		}
		fmt.Fprintf(out, "rolled back  %s (%s)\n", name(result.Source), result.Duration)
	case MigrateStatus:
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("db: migrate status: %w", err)
		}
		for _, s := range statuses {
			if s.State == goose.StateApplied {
				fmt.Fprintf(out, "applied      %s  %s\n", name(s.Source), s.AppliedAt.UTC().Format("2006-01-02 15:04:05Z"))
				continue
			}
			fmt.Fprintf(out, "%-11s  %s\n", s.State, name(s.Source))
		}
	}
	return nil
}

func name(source *goose.Source) string {
	return filepath.Base(source.Path)
}
