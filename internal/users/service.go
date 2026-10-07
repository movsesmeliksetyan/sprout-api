// Package users manages user provisioning, profile, preferences and stats.
package users

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// provisionAttempts bounds how often Provision starts over after losing the
// race to create a user whose creation was then rolled back.
const provisionAttempts = 3

// Database is what the service needs from the pool: queries, and
// transactions for the writes that must happen together.
type Database interface {
	db.DBTX
	db.Beginner
}

// CreatedHook runs once for every new user, inside the transaction that
// creates it: q reads and writes through that transaction, and an error
// undoes the user.
type CreatedHook func(ctx context.Context, q db.Querier, user db.User) error

// Service holds the business logic for users.
type Service struct {
	db        Database
	onCreated CreatedHook
}

// Option customises a Service.
type Option func(*Service)

// WithOnUserCreated sets the hook that runs when a user is created.
func WithOnUserCreated(hook CreatedHook) Option {
	return func(s *Service) { s.onCreated = hook }
}

// NewService returns a Service working on database.
func NewService(database Database, opts ...Option) *Service {
	s := &Service{db: database}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Provision returns the user that claims.Subject identifies, creating it on
// first sight. Concurrent first calls for one subject create one user and run
// the created hook once. An existing user is returned as stored: claims never
// overwrite a profile.
func (s *Service) Provision(ctx context.Context, claims auth.Claims) (db.User, error) {
	if claims.Subject == "" {
		return db.User{}, errors.New("users: provision: claims have no subject")
	}

	for range provisionAttempts {
		user, err := db.New(s.db).GetUserByAuth0Sub(ctx, claims.Subject)
		if err == nil {
			return user, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, fmt.Errorf("users: find by subject: %w", err)
		}

		user, created, err := s.create(ctx, claims)
		if err != nil {
			return db.User{}, err
		}
		if created {
			return user, nil
		}
		// Another request is creating this user. The insert waited for it, so
		// the user is there now, unless that request rolled back.
	}
	return db.User{}, errors.New("users: provision: user could not be created")
}

// create inserts the user and runs the created hook in one transaction.
// created is false when the subject was taken by a concurrent request.
func (s *Service) create(ctx context.Context, claims auth.Claims) (user db.User, created bool, err error) {
	id, err := uuid.NewV7()
	if err != nil {
		return db.User{}, false, fmt.Errorf("users: new id: %w", err)
	}

	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		user, err = q.CreateUser(ctx, db.CreateUserParams{
			ID:       id,
			Auth0Sub: claims.Subject,
			Email:    strings.TrimSpace(claims.Email),
			Name:     initialName(claims.Name),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("users: create: %w", err)
		}
		created = true

		if s.onCreated != nil {
			if err := s.onCreated(ctx, q, user); err != nil {
				return fmt.Errorf("users: created hook: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return db.User{}, false, err
	}
	return user, created, nil
}

// initialName is the name a new user starts with. Auth0 fills the name of a
// user who has none, such as one who signed in with an email code, with their
// email address; that is not a name, so it is dropped and the user gives a
// real one during onboarding.
func initialName(claim string) string {
	name := strings.TrimSpace(claim)
	if strings.Contains(name, "@") {
		return ""
	}
	return name
}

// FirstName returns the first word of a full name.
func FirstName(name string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(name), " ")
	return first
}

type userKey struct{}

// WithUser returns a context carrying the request's user.
func WithUser(ctx context.Context, user db.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// FromContext returns the user put there by Middleware. ok is false outside
// an authenticated request.
func FromContext(ctx context.Context) (user db.User, ok bool) {
	user, ok = ctx.Value(userKey{}).(db.User)
	return user, ok
}
