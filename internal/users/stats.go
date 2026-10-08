package users

import (
	"context"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// Stats are the figures shown on the profile.
type Stats struct {
	// CategoriesCount is how many active categories the user has.
	CategoriesCount int
	GoalsCount      int
	// StreakDays is how many calendar days in a row, ending today or
	// yesterday, the user added at least one transaction on. A day without
	// one yet does not break the streak until it is over.
	StreakDays int
}

// Stats counts what the user's profile shows. Days are the user's calendar
// days in the timezone they have now.
func (s *Service) Stats(ctx context.Context, user db.User) (Stats, error) {
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return Stats{}, fmt.Errorf("users: timezone %q: %w", user.Timezone, err)
	}
	q := db.New(s.db)

	categories, err := q.CountActiveCategories(ctx, user.ID)
	if err != nil {
		return Stats{}, fmt.Errorf("users: count categories: %w", err)
	}
	// Contributions to goals join the streak once goals exist (BE-26).
	streak, err := q.ActivityStreak(ctx, db.ActivityStreakParams{
		UserID:   user.ID,
		Timezone: user.Timezone,
		Today:    period.LocalDate(s.now(), loc),
	})
	if err != nil {
		return Stats{}, fmt.Errorf("users: streak: %w", err)
	}

	return Stats{
		CategoriesCount: int(categories),
		// Counted once goals exist (BE-25).
		GoalsCount: 0,
		StreakDays: int(streak),
	}, nil
}
