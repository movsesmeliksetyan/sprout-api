package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Beginner starts a transaction. A pool begins a real one; a pgx.Tx begins a
// savepoint, so WithTx can be nested.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx runs fn in a transaction: committed when fn returns nil, rolled
// back when it returns an error or panics. fn gets the transaction itself;
// wrap it with New for queries.
func WithTx(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, db, fn)
}
