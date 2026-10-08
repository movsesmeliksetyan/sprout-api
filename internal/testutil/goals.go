package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// GoalOption changes the goal NewGoal creates.
type GoalOption func(*db.UpdateGoalParams)

// GoalTitle sets the title in place of a generated one.
func GoalTitle(title string) GoalOption {
	return func(p *db.UpdateGoalParams) { p.Title = title }
}

// GoalTarget sets the target, which is 10000 otherwise.
func GoalTarget(minor int64) GoalOption {
	return func(p *db.UpdateGoalParams) { p.TargetMinor = minor }
}

// GoalStatus sets the status, which is active otherwise. A completed goal
// is recorded as completed just now.
func GoalStatus(status string) GoalOption {
	return func(p *db.UpdateGoalParams) {
		p.Status = status
		if status == "completed" {
			now := time.Now()
			p.CompletedAt = &now
		}
	}
}

// NewGoal stores a goal for the user, after the ones they have. It has a
// title of its own and a target of 10000 unless the options say otherwise.
func NewGoal(t testing.TB, database db.DBTX, userID uuid.UUID, opts ...GoalOption) db.Goal {
	t.Helper()
	ctx := context.Background()
	q := db.New(database)

	id, err := uuid.NewV7()
	require.NoError(t, err)
	sortOrder, err := q.NextGoalSortOrder(ctx, userID)
	require.NoError(t, err)
	params := db.UpdateGoalParams{
		ID:          id,
		UserID:      userID,
		Title:       "Goal " + id.String()[24:],
		TargetMinor: 10000,
		Status:      "active",
		SortOrder:   sortOrder,
	}
	for _, opt := range opts {
		opt(&params)
	}

	_, err = q.CreateGoal(ctx, db.CreateGoalParams{
		ID: id, UserID: userID, Title: params.Title, TargetMinor: params.TargetMinor, SortOrder: params.SortOrder,
	})
	require.NoError(t, err)
	goal, err := q.UpdateGoal(ctx, params)
	require.NoError(t, err)
	return goal
}

// NewContribution stores a contribution of the given kind ("topup" or
// "withdrawal") to one of the user's goals at occurredAt. It does not
// complete or reopen the goal.
func NewContribution(t testing.TB, database db.DBTX, user db.User, goalID uuid.UUID, kind string, amountMinor int64, occurredAt time.Time) db.GoalContribution {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	loc, err := time.LoadLocation(user.Timezone)
	require.NoError(t, err)

	contribution, err := db.New(database).CreateContribution(context.Background(), db.CreateContributionParams{
		ID:          id,
		UserID:      user.ID,
		GoalID:      goalID,
		Kind:        kind,
		AmountMinor: amountMinor,
		OccurredAt:  occurredAt,
		LocalDate:   period.LocalDate(occurredAt, loc),
	})
	require.NoError(t, err)
	return contribution
}
