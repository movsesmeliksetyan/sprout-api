// Package session carries the signed-in user through a request's context.
// It is a leaf package so that every feature can read the user without
// depending on the package that provisions it.
package session

import (
	"context"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

type userKey struct{}

// WithUser returns a context carrying the request's user.
func WithUser(ctx context.Context, user db.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// User returns the user put in the context by users.Middleware. ok is false
// outside an authenticated request.
func User(ctx context.Context) (user db.User, ok bool) {
	user, ok = ctx.Value(userKey{}).(db.User)
	return user, ok
}
