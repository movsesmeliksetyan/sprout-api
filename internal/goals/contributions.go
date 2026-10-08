package goals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

const (
	// DefaultLimit is the page size when the request names none.
	DefaultLimit = 50
	// MaxLimit is the largest page a request can ask for.
	MaxLimit = 200

	// maxAmountMinor bounds one contribution, as it bounds a target.
	maxAmountMinor = maxTargetMinor
)

// ContributionKind is the direction of a contribution.
type ContributionKind string

// The kinds of contribution (contract §1.3).
const (
	KindTopup      ContributionKind = "topup"
	KindWithdrawal ContributionKind = "withdrawal"
)

// CompletionHook runs when a goal becomes completed, inside the database
// transaction that records it: q reads and writes through that transaction,
// and an error undoes the change.
type CompletionHook func(ctx context.Context, q db.Querier, goal db.Goal) error

// WithCompletionHook sets what happens when a goal becomes completed.
// Without it nothing does.
func WithCompletionHook(hook CompletionHook) Option {
	return func(s *Service) { s.onCompleted = hook }
}

// ContributionParams describes a new contribution. A nil OccurredAt is now.
type ContributionParams struct {
	Kind        ContributionKind
	AmountMinor int64
	OccurredAt  *time.Time
}

// ContributionPage is one page of a goal's contributions, newest first.
type ContributionPage struct {
	Items []db.GoalContribution
	// Next is where the following page starts; nil on the last page.
	Next *httpx.Cursor
}

// Contribute moves money into one of the user's goals or back out of it,
// and returns the contribution with the goal after it. The goal is completed
// once the target is reached and active again when a withdrawal takes it
// below; an archived goal stays archived. Invalid values, a withdrawal of
// more than is saved among them, are reported in an *httpx.ValidationError.
func (s *Service) Contribute(ctx context.Context, user db.User, goalID uuid.UUID, params ContributionParams) (db.GoalContribution, Goal, error) {
	loc, err := location(user)
	if err != nil {
		return db.GoalContribution{}, Goal{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.GoalContribution{}, Goal{}, fmt.Errorf("goals: new id: %w", err)
	}

	fields := map[string]string{}
	if params.Kind != KindTopup && params.Kind != KindWithdrawal {
		fields["kind"] = "must be topup or withdrawal"
	}
	switch {
	case params.AmountMinor <= 0:
		fields["amount_minor"] = "must be > 0"
	case params.AmountMinor > maxAmountMinor:
		fields["amount_minor"] = "is out of range"
	}
	if len(fields) > 0 {
		return db.GoalContribution{}, Goal{}, &httpx.ValidationError{Fields: fields}
	}

	occurredAt := s.now()
	if params.OccurredAt != nil {
		occurredAt = *params.OccurredAt
	}

	var created db.GoalContribution
	var goal Goal
	err = s.write(ctx, user.ID, func(q *db.Queries) error {
		row, err := getGoal(ctx, q, user.ID, goalID)
		if err != nil {
			return err
		}
		saved := row.SavedMinor + signed(string(params.Kind), params.AmountMinor)
		if saved < 0 {
			return &httpx.ValidationError{Fields: map[string]string{"amount_minor": "must not exceed what is saved"}}
		}
		created, err = q.CreateContribution(ctx, db.CreateContributionParams{
			ID:          id,
			UserID:      user.ID,
			GoalID:      goalID,
			Kind:        string(params.Kind),
			AmountMinor: params.AmountMinor,
			OccurredAt:  occurredAt,
			LocalDate:   period.LocalDate(occurredAt, loc),
		})
		if err != nil {
			return fmt.Errorf("goals: create contribution: %w", err)
		}
		goal, err = s.resettle(ctx, q, row.Goal, saved)
		if err != nil {
			return err
		}
		return s.pace(ctx, q, user, &goalID, &goal)
	})
	if err != nil {
		return db.GoalContribution{}, Goal{}, err
	}
	return created, goal, nil
}

// ListContributions returns a page of the contributions of one of the
// user's goals ordered by local date and id, newest first. A nil limit is
// DefaultLimit, and a nil after starts at the newest contribution.
func (s *Service) ListContributions(ctx context.Context, userID, goalID uuid.UUID, limit *int, after *httpx.Cursor) (ContributionPage, error) {
	size := DefaultLimit
	if limit != nil {
		size = *limit
	}
	if size < 1 || size > MaxLimit {
		return ContributionPage{}, httpx.WithMessage(httpx.ErrBadRequest,
			fmt.Sprintf("The limit must be between 1 and %d.", MaxLimit))
	}
	q := db.New(s.db)
	if _, err := getGoal(ctx, q, userID, goalID); err != nil {
		return ContributionPage{}, err
	}

	list := db.ListContributionsParams{
		GoalID: goalID,
		UserID: userID,
		// One row more than the page tells whether another page follows.
		RowLimit: int32(size) + 1,
	}
	if after != nil {
		list.CursorDate = &after.LocalDate
		list.CursorID = &after.ID
	}
	items, err := q.ListContributions(ctx, list)
	if err != nil {
		return ContributionPage{}, fmt.Errorf("goals: list contributions: %w", err)
	}
	page := ContributionPage{Items: items}
	if len(items) > size {
		page.Items = items[:size]
		last := page.Items[size-1]
		page.Next = &httpx.Cursor{LocalDate: last.LocalDate, ID: last.ID}
	}
	return page, nil
}

// DeleteContribution removes a contribution from one of the user's goals
// and returns the goal after it, completed or active again as what is saved
// now says. A goal or contribution that is missing or someone else's is
// httpx.ErrNotFound. A top-up that later withdrawals depend on cannot go: it
// would leave less than nothing saved, which is httpx.ErrConflict.
func (s *Service) DeleteContribution(ctx context.Context, user db.User, goalID, id uuid.UUID) (Goal, error) {
	userID := user.ID
	var goal Goal
	err := s.write(ctx, userID, func(q *db.Queries) error {
		row, err := getGoal(ctx, q, userID, goalID)
		if err != nil {
			return err
		}
		deleted, err := q.DeleteContribution(ctx, db.DeleteContributionParams{ID: id, GoalID: goalID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("goals: delete contribution: %w", err)
		}
		saved := row.SavedMinor - signed(deleted.Kind, deleted.AmountMinor)
		if saved < 0 {
			return httpx.WithMessage(httpx.ErrConflict,
				"This top-up cannot be removed: more has been withdrawn from the goal than would be left in it.")
		}
		goal, err = s.resettle(ctx, q, row.Goal, saved)
		if err != nil {
			return err
		}
		return s.pace(ctx, q, user, &goalID, &goal)
	})
	if err != nil {
		return Goal{}, err
	}
	return goal, nil
}

// resettle gives the goal the status that savedMinor against its target
// calls for, storing it only when it changed.
func (s *Service) resettle(ctx context.Context, q *db.Queries, goal db.Goal, savedMinor int64) (Goal, error) {
	status, completedAt := settle(Status(goal.Status) == StatusArchived, savedMinor, goal.TargetMinor, goal.CompletedAt, s.now())
	if string(status) == goal.Status && sameTime(completedAt, goal.CompletedAt) {
		return newGoal(goal, savedMinor), nil
	}
	updated, err := q.UpdateGoal(ctx, db.UpdateGoalParams{
		ID:          goal.ID,
		UserID:      goal.UserID,
		Title:       goal.Title,
		Emoji:       goal.Emoji,
		ImageKey:    goal.ImageKey,
		TargetMinor: goal.TargetMinor,
		Status:      string(status),
		SortOrder:   goal.SortOrder,
		CompletedAt: completedAt,
	})
	if err != nil {
		return Goal{}, fmt.Errorf("goals: settle: %w", err)
	}
	if err := s.completed(ctx, q, goal, updated); err != nil {
		return Goal{}, err
	}
	return newGoal(updated, savedMinor), nil
}

// completed runs the completion hook when the change from previous to
// updated is the goal becoming completed.
func (s *Service) completed(ctx context.Context, q db.Querier, previous, updated db.Goal) error {
	if previous.CompletedAt != nil || updated.CompletedAt == nil {
		return nil
	}
	if err := s.onCompleted(ctx, q, updated); err != nil {
		return fmt.Errorf("goals: completion hook: %w", err)
	}
	return nil
}

// getGoal reads one of the user's goals with what is saved in it. A goal
// that is missing or someone else's is httpx.ErrNotFound.
func getGoal(ctx context.Context, q *db.Queries, userID, id uuid.UUID) (db.GetGoalRow, error) {
	row, err := q.GetGoal(ctx, db.GetGoalParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GetGoalRow{}, httpx.ErrNotFound
	}
	if err != nil {
		return db.GetGoalRow{}, fmt.Errorf("goals: get: %w", err)
	}
	return row, nil
}

// signed is what a contribution adds to what is saved: a withdrawal takes
// its amount away.
func signed(kind string, amountMinor int64) int64 {
	if ContributionKind(kind) == KindWithdrawal {
		return -amountMinor
	}
	return amountMinor
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
