package goals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
	"github.com/movsesmeliksetyan/sprout-api/internal/uploads"
)

const (
	// MaxActive is how many active goals a user can have. Completed and
	// archived goals do not count.
	MaxActive = 20

	maxTitleLength = 40
	maxEmojiLength = 16
	maxTargetMinor = 1_000_000_000_000

	// recentContributions is how many contributions a goal's detail shows.
	recentContributions = 5
	// imageURLLifetime is how long the image_url in a response works.
	imageURLLifetime = time.Hour
)

// Status is where a goal is in its life.
type Status string

// The statuses of the contract §1.3. A goal that is not archived is
// completed exactly when what is saved has reached the target.
const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusArchived  Status = "archived"
)

// Database is what the service needs from the pool: queries, and
// transactions for the writes.
type Database interface {
	db.DBTX
	db.Beginner
}

// Service holds the business logic for goals.
type Service struct {
	db          Database
	now         func() time.Time
	images      *images
	onCompleted CompletionHook
}

// images is what the service needs to keep goal pictures.
type images struct {
	uploads *uploads.Service
	store   storage.Store
	logger  *slog.Logger
}

// Option customises a Service.
type Option func(*Service)

// WithImages lets goals take a picture from an upload. Without it no upload
// can be used as one.
func WithImages(uploadService *uploads.Service, store storage.Store, logger *slog.Logger) Option {
	return func(s *Service) {
		s.images = &images{uploads: uploadService, store: store, logger: logger}
	}
}

// WithClock sets the time a goal is recorded as completed at, and the time a
// contribution without one is given.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService returns a Service backed by database.
func NewService(database Database, opts ...Option) *Service {
	s := &Service{
		db:          database,
		now:         time.Now,
		onCompleted: func(context.Context, db.Querier, db.Goal) error { return nil },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Goal is a stored goal with the figures derived from its contributions.
type Goal struct {
	db.Goal
	// SavedMinor is top-ups minus withdrawals.
	SavedMinor int64
	// RemainingMinor is what is left to save; never below zero.
	RemainingMinor int64
	// Pct is the part of the target that is saved, in whole percent; never
	// above 100.
	Pct int
	// MonthlyPaceMinor and EtaMonth are computed once pace exists (BE-27).
	MonthlyPaceMinor int64
	EtaMonth         *string
}

func newGoal(goal db.Goal, savedMinor int64) Goal {
	return Goal{
		Goal:           goal,
		SavedMinor:     savedMinor,
		RemainingMinor: max(goal.TargetMinor-savedMinor, 0),
		Pct:            savedPct(savedMinor, goal.TargetMinor),
	}
}

// savedPct is saved as a whole percentage of target, between 0 and 100.
func savedPct(savedMinor, targetMinor int64) int {
	pct, _ := money.Percent(savedMinor, targetMinor)
	return min(max(pct, 0), 100)
}

// List is the user's goals with the totals across them.
type List struct {
	TotalSavedMinor  int64
	TotalTargetMinor int64
	// Pct is the part of the total target that is saved; 0 without goals.
	Pct   int
	Items []Goal
}

// Detail is a goal with its newest contributions, newest first.
type Detail struct {
	Goal
	RecentContributions []db.GoalContribution
}

// List returns the user's active and completed goals in display order.
func (s *Service) List(ctx context.Context, userID uuid.UUID) (List, error) {
	rows, err := db.New(s.db).ListGoals(ctx, userID)
	if err != nil {
		return List{}, fmt.Errorf("goals: list: %w", err)
	}
	list := List{Items: make([]Goal, 0, len(rows))}
	for _, row := range rows {
		list.Items = append(list.Items, newGoal(row.Goal, row.SavedMinor))
		list.TotalSavedMinor += row.SavedMinor
		list.TotalTargetMinor += row.Goal.TargetMinor
	}
	list.Pct = savedPct(list.TotalSavedMinor, list.TotalTargetMinor)
	return list, nil
}

// Get returns one of the user's goals, archived or not, with its newest
// contributions. A goal that is missing or someone else's is
// httpx.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (Detail, error) {
	q := db.New(s.db)
	row, err := q.GetGoal(ctx, db.GetGoalParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, httpx.ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("goals: get: %w", err)
	}
	recent, err := q.ListRecentContributions(ctx, db.ListRecentContributionsParams{
		GoalID: id, UserID: userID, RowLimit: recentContributions,
	})
	if err != nil {
		return Detail{}, fmt.Errorf("goals: recent contributions: %w", err)
	}
	return Detail{Goal: newGoal(row.Goal, row.SavedMinor), RecentContributions: recent}, nil
}

// CreateParams describes a new goal.
type CreateParams struct {
	Title string
	Emoji *string
	// ImageUploadID is the goal_image upload to use as the picture.
	ImageUploadID *uuid.UUID
	TargetMinor   int64
}

// Create adds a goal after the user's others. Invalid values are reported
// together in an *httpx.ValidationError; a full list of active goals is
// httpx.ErrConflict.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, params CreateParams) (Goal, error) {
	fields := map[string]string{}
	title := checkTitle(params.Title, fields)
	emoji := checkEmoji(params.Emoji, fields)
	checkTarget(params.TargetMinor, fields)
	if len(fields) > 0 {
		return Goal{}, &httpx.ValidationError{Fields: fields}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Goal{}, fmt.Errorf("goals: new id: %w", err)
	}
	var created db.Goal
	err = s.write(ctx, userID, func(q *db.Queries) error {
		if err := checkRoom(ctx, q, userID); err != nil {
			return err
		}
		imageKey, err := s.claimImage(ctx, q, userID, params.ImageUploadID)
		if err != nil {
			return err
		}
		sortOrder, err := q.NextGoalSortOrder(ctx, userID)
		if err != nil {
			return fmt.Errorf("goals: next sort order: %w", err)
		}
		created, err = q.CreateGoal(ctx, db.CreateGoalParams{
			ID:          id,
			UserID:      userID,
			Title:       title,
			Emoji:       emoji,
			ImageKey:    imageKey,
			TargetMinor: params.TargetMinor,
			SortOrder:   sortOrder,
		})
		if err != nil {
			return fmt.Errorf("goals: create: %w", err)
		}
		return nil
	})
	if err != nil {
		return Goal{}, err
	}
	return newGoal(created, 0), nil
}

// Patch is a partial update: a nil field is left unchanged.
type Patch struct {
	Title *string
	// Emoji replaces the emoji; ClearEmoji removes it.
	Emoji      *string
	ClearEmoji bool
	// ImageUploadID is the goal_image upload to use as the picture;
	// ClearImage removes the current one.
	ImageUploadID *uuid.UUID
	ClearImage    bool
	TargetMinor   *int64
	// Status archives the goal or brings it back; it cannot complete one.
	Status    *Status
	SortOrder *int
}

// Update applies patch to one of the user's goals and returns the result.
// Whether a goal that is not archived is completed follows from what is
// saved against the target, which the patch may have moved. Invalid values
// are reported together in an *httpx.ValidationError; bringing a goal back
// into a full list of active ones is httpx.ErrConflict.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, patch Patch) (Goal, error) {
	fields := map[string]string{}
	var title, emoji *string
	if patch.Title != nil {
		checked := checkTitle(*patch.Title, fields)
		title = &checked
	}
	if patch.Emoji != nil {
		emoji = checkEmoji(patch.Emoji, fields)
	}
	if patch.TargetMinor != nil {
		checkTarget(*patch.TargetMinor, fields)
	}
	if patch.Status != nil {
		switch *patch.Status {
		case StatusActive, StatusArchived:
		case StatusCompleted:
			fields["status"] = "is set when the goal is fully saved"
		default:
			fields["status"] = "must be active or archived"
		}
	}
	if patch.SortOrder != nil && (*patch.SortOrder < 0 || *patch.SortOrder > math.MaxInt32) {
		fields["sort_order"] = "must not be negative"
	}
	if len(fields) > 0 {
		return Goal{}, &httpx.ValidationError{Fields: fields}
	}

	var previous, updated db.Goal
	var saved int64
	err := s.write(ctx, userID, func(q *db.Queries) error {
		row, err := q.GetGoal(ctx, db.GetGoalParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("goals: get: %w", err)
		}
		previous, saved = row.Goal, row.SavedMinor

		params := db.UpdateGoalParams{
			ID:          id,
			UserID:      userID,
			Title:       previous.Title,
			Emoji:       previous.Emoji,
			ImageKey:    previous.ImageKey,
			TargetMinor: previous.TargetMinor,
			Status:      previous.Status,
			SortOrder:   previous.SortOrder,
			CompletedAt: previous.CompletedAt,
		}
		if title != nil {
			params.Title = *title
		}
		if patch.Emoji != nil || patch.ClearEmoji {
			params.Emoji = emoji
		}
		if patch.TargetMinor != nil {
			params.TargetMinor = *patch.TargetMinor
		}
		if patch.SortOrder != nil {
			params.SortOrder = int32(*patch.SortOrder)
		}

		archived := Status(previous.Status) == StatusArchived
		if patch.Status != nil {
			archived = *patch.Status == StatusArchived
		}
		status, completedAt := settle(archived, saved, params.TargetMinor, previous.CompletedAt, s.now())
		params.Status, params.CompletedAt = string(status), completedAt
		if Status(previous.Status) == StatusArchived && status == StatusActive {
			if err := checkRoom(ctx, q, userID); err != nil {
				return err
			}
		}

		if patch.ClearImage {
			params.ImageKey = nil
		}
		if patch.ImageUploadID != nil {
			params.ImageKey, err = s.claimImage(ctx, q, userID, patch.ImageUploadID)
			if err != nil {
				return err
			}
		}

		updated, err = q.UpdateGoal(ctx, params)
		if err != nil {
			return fmt.Errorf("goals: update: %w", err)
		}
		return s.completed(ctx, q, previous, updated)
	})
	if err != nil {
		return Goal{}, err
	}

	if previous.ImageKey != nil && (updated.ImageKey == nil || *updated.ImageKey != *previous.ImageKey) {
		s.deleteImage(ctx, *previous.ImageKey)
	}
	return newGoal(updated, saved), nil
}

// Delete removes one of the user's goals with its contributions and its
// picture. A goal that is missing or someone else's is httpx.ErrNotFound.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	var deleted db.Goal
	err := s.write(ctx, userID, func(q *db.Queries) error {
		row, err := q.GetGoal(ctx, db.GetGoalParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("goals: get: %w", err)
		}
		deleted = row.Goal
		if _, err := q.DeleteGoal(ctx, db.DeleteGoalParams{ID: id, UserID: userID}); err != nil {
			return fmt.Errorf("goals: delete: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deleted.ImageKey != nil {
		s.deleteImage(ctx, *deleted.ImageKey)
	}
	return nil
}

// ImageURL returns a link to the goal's picture that works for an hour, or
// nil when it has none.
func (s *Service) ImageURL(ctx context.Context, goal db.Goal) (*string, error) {
	if s.images == nil || goal.ImageKey == nil {
		return nil, nil
	}
	url, err := s.images.store.PresignGet(ctx, *goal.ImageKey, imageURLLifetime)
	if err != nil {
		return nil, fmt.Errorf("goals: image url: %w", err)
	}
	return &url, nil
}

// settle returns the status a goal has with savedMinor against targetMinor,
// and when it became completed: an archived goal stays as it is, any other
// is completed exactly when the target is reached.
func settle(archived bool, savedMinor, targetMinor int64, completedAt *time.Time, now time.Time) (Status, *time.Time) {
	switch {
	case archived:
		return StatusArchived, completedAt
	case savedMinor < targetMinor:
		return StatusActive, nil
	case completedAt != nil:
		return StatusCompleted, completedAt
	}
	return StatusCompleted, &now
}

// write runs fn in a transaction that holds the user's row, so that one
// user's goal changes happen one at a time: the limit and the order are then
// checked against a list that cannot move.
func (s *Service) write(ctx context.Context, userID uuid.UUID, fn func(q *db.Queries) error) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.GetUserForUpdate(ctx, userID); err != nil {
			return fmt.Errorf("goals: lock user: %w", err)
		}
		return fn(q)
	})
}

// checkRoom refuses another active goal once the user has MaxActive.
func checkRoom(ctx context.Context, q *db.Queries, userID uuid.UUID) error {
	count, err := q.CountActiveGoals(ctx, userID)
	if err != nil {
		return fmt.Errorf("goals: count: %w", err)
	}
	if count >= MaxActive {
		return httpx.WithMessage(httpx.ErrConflict,
			fmt.Sprintf("You can have at most %d active goals. Archive or delete one first.", MaxActive))
	}
	return nil
}

// claimImage turns a goal_image upload into the object key of a goal's
// picture; a nil id is no picture. A refused upload is an
// *httpx.ValidationError on image_upload_id.
func (s *Service) claimImage(ctx context.Context, q db.Querier, userID uuid.UUID, uploadID *uuid.UUID) (*string, error) {
	if uploadID == nil {
		return nil, nil
	}
	if s.images == nil {
		return nil, imageError("upload not found")
	}
	upload, err := s.images.uploads.Consume(ctx, q, userID, *uploadID, uploads.PurposeGoalImage)
	if reason, refused := uploads.Rejection(err); refused {
		return nil, imageError(reason)
	}
	if err != nil {
		return nil, err
	}
	return &upload.ObjectKey, nil
}

func imageError(reason string) error {
	return &httpx.ValidationError{Fields: map[string]string{"image_upload_id": reason}}
}

// deleteImage removes a picture no goal has any more. The goal is already
// saved, so a failure is logged and left for a later clean-up.
func (s *Service) deleteImage(ctx context.Context, key string) {
	if s.images == nil {
		return
	}
	if err := s.images.store.Delete(ctx, key); err != nil {
		s.images.logger.WarnContext(ctx, "could not delete a goal's image",
			slog.String("object_key", key),
			slog.String("error", err.Error()),
		)
	}
}

// checkTitle returns title in the form it is stored in, recording in fields
// why it cannot be used.
func checkTitle(title string, fields map[string]string) string {
	title = strings.TrimSpace(title)
	switch length := utf8.RuneCountInString(title); {
	case length == 0:
		fields["title"] = "must not be empty"
	case length > maxTitleLength:
		fields["title"] = fmt.Sprintf("must be at most %d characters", maxTitleLength)
	}
	return title
}

// checkEmoji returns emoji in the form it is stored in: trimmed, and nil
// when there is none.
func checkEmoji(emoji *string, fields map[string]string) *string {
	if emoji == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*emoji)
	if trimmed == "" {
		return nil
	}
	if utf8.RuneCountInString(trimmed) > maxEmojiLength {
		fields["emoji"] = fmt.Sprintf("must be at most %d characters", maxEmojiLength)
	}
	return &trimmed
}

func checkTarget(targetMinor int64, fields map[string]string) {
	switch {
	case targetMinor <= 0:
		fields["target_minor"] = "must be greater than zero"
	case targetMinor > maxTargetMinor:
		fields["target_minor"] = fmt.Sprintf("must be at most %d", maxTargetMinor)
	}
}
