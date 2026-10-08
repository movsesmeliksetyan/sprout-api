package goals

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

const (
	// paceWindowDays is how far back the pace looks: the calendar days up to
	// and including today.
	paceWindowDays = 90
	// daysPerMonth is the month the pace is an average over: the window is
	// three of them.
	daysPerMonth = 30
	// maxEtaMonths is how far ahead an ETA can be. A goal that would take
	// longer at its pace has none.
	maxEtaMonths = 1200
)

// pace fills in the monthly pace and the ETA of the given goals of the
// user, read through q. A nil goalID reads the contributions of every goal;
// otherwise only that goal's are read, which must be the one given.
func (s *Service) pace(ctx context.Context, q db.Querier, user db.User, goalID *uuid.UUID, goals ...*Goal) error {
	if len(goals) == 0 {
		return nil
	}
	loc, err := location(user)
	if err != nil {
		return err
	}
	today := period.LocalDate(s.now(), loc)
	rows, err := q.ContributedBetween(ctx, db.ContributedBetweenParams{
		UserID:   user.ID,
		GoalID:   goalID,
		FromDate: today.AddDate(0, 0, -(paceWindowDays - 1)),
		ToDate:   today,
	})
	if err != nil {
		return fmt.Errorf("goals: contributed in the pace window: %w", err)
	}
	net := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		net[row.GoalID] = row.NetMinor
	}
	for _, goal := range goals {
		goal.MonthlyPaceMinor = monthlyPace(net[goal.ID], period.LocalDate(goal.CreatedAt, loc), today)
		goal.EtaMonth = etaMonth(goal.RemainingMinor, goal.MonthlyPaceMinor, today)
	}
	return nil
}

// monthlyPace is what a goal gains in a month of 30 days, given netMinor
// gained in the pace window: a third of it for a goal as old as the window,
// and for a younger one the same average over the days it has existed,
// counted as at least a month. It is negative when more was withdrawn than
// topped up. created and today are calendar days.
func monthlyPace(netMinor int64, created, today time.Time) int64 {
	days := int64(today.Sub(created) / (24 * time.Hour))
	days = min(max(days, daysPerMonth), paceWindowDays)
	return money.DivRound(netMinor*daysPerMonth, days)
}

// etaMonth is the month, as YYYY-MM, in which a goal is fully saved at
// paceMinor a month: the month of today plus the months remainingMinor
// takes, rounded up. It is nil when nothing remains, when the pace is not
// positive and when the month is more than maxEtaMonths away.
func etaMonth(remainingMinor, paceMinor int64, today time.Time) *string {
	if remainingMinor <= 0 || paceMinor <= 0 {
		return nil
	}
	months := (remainingMinor-1)/paceMinor + 1
	if months > maxEtaMonths {
		return nil
	}
	index := int64(today.Year())*12 + int64(today.Month()) - 1 + months
	eta := fmt.Sprintf("%04d-%02d", index/12, index%12+1)
	return &eta
}

func location(user db.User) (*time.Location, error) {
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return nil, fmt.Errorf("goals: timezone %q: %w", user.Timezone, err)
	}
	return loc, nil
}
