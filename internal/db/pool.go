package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open returns a connection pool for databaseURL. It does not connect:
// connections are made on first use, so a process can start while the
// database is still coming up and report that through Ready.
//
// Pool size and timeouts can be tuned with pgxpool's URL parameters, such
// as pool_max_conns.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// The parser's message can quote the URL, which holds the password.
		return nil, errors.New("db: database URL could not be parsed")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	return pool, nil
}

// Ready returns a readiness check that passes when a query can be run.
func Ready(pool *pgxpool.Pool) func(ctx context.Context) error {
	queries := New(pool)
	return func(ctx context.Context) error {
		_, err := queries.Ping(ctx)
		return err
	}
}
