// Package users manages user provisioning, profile, preferences and stats.
package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

const (
	// provisionAttempts bounds how often Provision starts over after losing
	// the race to create a user whose creation was then rolled back.
	provisionAttempts = 3

	maxNameLength = 100
	// maxStartingBalanceMinor bounds the starting balance in either
	// direction, far below where sums of amounts could overflow.
	maxStartingBalanceMinor = 1_000_000_000_000

	// avatarURLLifetime is how long the avatar_url in a response works.
	avatarURLLifetime = time.Hour
)

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

// TransactionCheck reports whether the user has any transactions. q reads
// through the transaction that is about to change the user.
type TransactionCheck func(ctx context.Context, q db.Querier, userID uuid.UUID) (bool, error)

// Service holds the business logic for users.
type Service struct {
	db              Database
	onCreated       CreatedHook
	hasTransactions TransactionCheck
	avatars         *avatars
}

// avatars is what the service needs to keep profile pictures.
type avatars struct {
	uploads *uploads.Service
	store   storage.Store
	logger  *slog.Logger
}

// Option customises a Service.
type Option func(*Service)

// WithOnUserCreated sets the hook that runs when a user is created.
func WithOnUserCreated(hook CreatedHook) Option {
	return func(s *Service) { s.onCreated = hook }
}

// WithTransactionCheck sets how the service learns that a user has
// transactions, which is when their currency can no longer change. Without
// it no user has any.
func WithTransactionCheck(check TransactionCheck) Option {
	return func(s *Service) { s.hasTransactions = check }
}

// WithAvatars lets users set a profile picture from an upload. Without it no
// upload can be used as an avatar.
func WithAvatars(uploadService *uploads.Service, store storage.Store, logger *slog.Logger) Option {
	return func(s *Service) {
		s.avatars = &avatars{uploads: uploadService, store: store, logger: logger}
	}
}

// NewService returns a Service working on database.
func NewService(database Database, opts ...Option) *Service {
	s := &Service{
		db:              database,
		hasTransactions: func(context.Context, db.Querier, uuid.UUID) (bool, error) { return false, nil },
	}
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

// Patch is a partial update of a user's profile. A nil field is left as it
// is.
type Patch struct {
	Name                 *string
	Currency             *string
	Timezone             *string
	StartingBalanceMinor *int64
	OnboardingCompleted  *bool
	NotificationsEnabled *bool
	BudgetAlerts         *bool
	WeeklyRecap          *bool

	// AvatarUploadID is the avatar upload to use as the profile picture;
	// ClearAvatar removes the current one.
	AvatarUploadID *uuid.UUID
	ClearAvatar    bool
}

// Update applies patch to the user and returns the result. Invalid values
// are reported together in an *httpx.ValidationError and nothing is written.
// The currency cannot change once the user has transactions: their amounts
// were entered in it.
func (s *Service) Update(ctx context.Context, userID uuid.UUID, patch Patch) (db.User, error) {
	params, err := validate(patch)
	if err != nil {
		return db.User{}, err
	}
	params.ID = userID
	if patch.AvatarUploadID != nil && s.avatars == nil {
		return db.User{}, avatarError("upload not found")
	}

	var previous, updated db.User
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		previous, err = q.GetUserForUpdate(ctx, userID)
		if err != nil {
			return fmt.Errorf("users: lock for update: %w", err)
		}

		if params.Currency != nil && *params.Currency != previous.Currency {
			locked, err := s.hasTransactions(ctx, q, userID)
			if err != nil {
				return fmt.Errorf("users: transaction check: %w", err)
			}
			if locked {
				return httpx.WithMessage(httpx.ErrConflict,
					"The currency cannot be changed once you have transactions.")
			}
		}

		if patch.AvatarUploadID != nil {
			upload, err := s.avatars.uploads.Consume(ctx, q, userID, *patch.AvatarUploadID, uploads.PurposeAvatar)
			if reason, refused := uploads.Rejection(err); refused {
				return avatarError(reason)
			}
			if err != nil {
				return err
			}
			params.AvatarKey = &upload.ObjectKey
		}

		updated, err = q.UpdateUser(ctx, params)
		if err != nil {
			return fmt.Errorf("users: update: %w", err)
		}
		return nil
	})
	if err != nil {
		return db.User{}, err
	}

	s.deleteReplacedAvatar(ctx, previous, updated)
	return updated, nil
}

func avatarError(reason string) error {
	return &httpx.ValidationError{Fields: map[string]string{"avatar_upload_id": reason}}
}

// deleteReplacedAvatar removes the picture a user no longer has. The profile
// is already saved, so a failure is logged and left for a later clean-up.
func (s *Service) deleteReplacedAvatar(ctx context.Context, previous, updated db.User) {
	if s.avatars == nil || previous.AvatarKey == nil {
		return
	}
	if updated.AvatarKey != nil && *updated.AvatarKey == *previous.AvatarKey {
		return
	}
	if err := s.avatars.store.Delete(ctx, *previous.AvatarKey); err != nil {
		s.avatars.logger.WarnContext(ctx, "could not delete the replaced avatar",
			slog.String("object_key", *previous.AvatarKey),
			slog.String("error", err.Error()),
		)
	}
}

// AvatarURL returns a link to the user's profile picture that works for an
// hour, or nil when they have none.
func (s *Service) AvatarURL(ctx context.Context, user db.User) (*string, error) {
	if s.avatars == nil || user.AvatarKey == nil {
		return nil, nil
	}
	url, err := s.avatars.store.PresignGet(ctx, *user.AvatarKey, avatarURLLifetime)
	if err != nil {
		return nil, fmt.Errorf("users: avatar url: %w", err)
	}
	return &url, nil
}

// validate checks patch and turns it into the update's arguments, with
// values in the form they are stored in.
func validate(patch Patch) (db.UpdateUserParams, error) {
	fields := map[string]string{}
	params := db.UpdateUserParams{
		StartingBalanceMinor: patch.StartingBalanceMinor,
		OnboardingCompleted:  patch.OnboardingCompleted,
		NotificationsEnabled: patch.NotificationsEnabled,
		BudgetAlerts:         patch.BudgetAlerts,
		WeeklyRecap:          patch.WeeklyRecap,
		ClearAvatar:          patch.ClearAvatar,
	}

	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		switch {
		case name == "":
			fields["name"] = "must not be empty"
		case utf8.RuneCountInString(name) > maxNameLength:
			fields["name"] = fmt.Sprintf("must be at most %d characters", maxNameLength)
		}
		params.Name = &name
	}

	if patch.Currency != nil {
		code := strings.ToUpper(strings.TrimSpace(*patch.Currency))
		if !money.Supported(code) {
			fields["currency"] = "is not a supported currency"
		}
		params.Currency = &code
	}

	if patch.Timezone != nil {
		zone := strings.TrimSpace(*patch.Timezone)
		if !validTimezone(zone) {
			fields["timezone"] = "must be an IANA timezone such as Europe/London"
		}
		params.Timezone = &zone
	}

	if b := patch.StartingBalanceMinor; b != nil && (*b > maxStartingBalanceMinor || *b < -maxStartingBalanceMinor) {
		fields["starting_balance_minor"] = "is out of range"
	}

	if len(fields) > 0 {
		return db.UpdateUserParams{}, &httpx.ValidationError{Fields: fields}
	}
	return params, nil
}

// validTimezone reports whether zone names an IANA timezone. The empty name
// and "Local" load too, but mean the server's zone, not the user's.
func validTimezone(zone string) bool {
	if zone == "" || zone == "Local" {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}

// FirstName returns the first word of a full name.
func FirstName(name string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(name), " ")
	return first
}
