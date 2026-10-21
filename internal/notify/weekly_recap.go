package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// RecapResult counts what a run of the weekly recap did.
type RecapResult struct {
	// Sent is how many recaps went out; Skipped, how many users had no
	// transaction last week or had theirs already.
	Sent    int
	Skipped int
}

// WeeklyRecaps sends last week's recap to every user with the recap on for
// whom it is Monday between 09:00 and 09:59 at now, in their timezone (PRD
// BE-58): the work of the hourly weekly_recap job. Users without any
// transaction last week get none. The dedup key names the week, so a run
// repeated within the hour sends nothing twice. One user's failure does not
// stop the others; the failures are returned together.
func (s *Service) WeeklyRecaps(ctx context.Context, now time.Time) (RecapResult, error) {
	users, err := db.New(s.db).ListRecapUsers(ctx, now)
	if err != nil {
		return RecapResult{}, fmt.Errorf("notify: recap users: %w", err)
	}
	var result RecapResult
	var failures []error
	for _, user := range users {
		sent, err := s.weeklyRecap(ctx, user, now)
		switch {
		case err != nil:
			failures = append(failures, fmt.Errorf("notify: recap for %s: %w", user.ID, err))
		case sent:
			result.Sent++
		default:
			result.Skipped++
		}
	}
	return result, errors.Join(failures...)
}

func (s *Service) weeklyRecap(ctx context.Context, user db.User, now time.Time) (bool, error) {
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return false, fmt.Errorf("timezone %q: %w", user.Timezone, err)
	}
	week, err := period.Resolve(period.Week, 1, loc, now)
	if err != nil {
		return false, err
	}
	before := week.Previous()
	q := db.New(s.db)
	count, err := q.CountTransactionsBetween(ctx, db.CountTransactionsBetweenParams{UserID: user.ID, FromDate: week.Start, ToDate: week.End})
	if err != nil || count == 0 {
		return false, err
	}
	spent, err := q.SpentBetween(ctx, db.SpentBetweenParams{UserID: user.ID, FromDate: week.Start, ToDate: week.End})
	if err != nil {
		return false, err
	}
	previous, err := q.SpentBetween(ctx, db.SpentBetweenParams{UserID: user.ID, FromDate: before.Start, ToDate: before.End})
	if err != nil {
		return false, err
	}
	categories, err := q.CategorySpending(ctx, db.CategorySpendingParams{
		UserID: user.ID, FromDate: week.Start, ToDate: week.End, PreviousFromDate: week.Start,
	})
	if err != nil {
		return false, err
	}
	var top *db.CategorySpendingRow
	if len(categories) > 0 && categories[0].SpentMinor > 0 {
		top = &categories[0]
	}
	return s.Send(ctx, user.ID, Notification{
		Kind:     KindWeeklyRecap,
		Title:    "Your week in review",
		Body:     recapBody(spent, previous, top, user.Currency),
		DeepLink: "sprout://stats",
		DedupKey: "weekly_recap:" + week.Start.Format(time.DateOnly),
	})
}

// recapBody is the recap's words: the week's spending, against the week
// before, and the category that took the most.
func recapBody(spent, previous int64, top *db.CategorySpendingRow, currency string) string {
	if spent == 0 {
		return "You spent nothing last week."
	}
	body := fmt.Sprintf("You spent %s last week", money.Format(spent, currency))
	pct, ok := money.PercentChange(spent, previous)
	switch {
	case !ok:
	case pct < 0:
		body += fmt.Sprintf(", %d%% less than the week before", -pct)
	case pct > 0:
		body += fmt.Sprintf(", %d%% more than the week before", pct)
	default:
		body += ", about the same as the week before"
	}
	body += "."
	if top != nil {
		body += fmt.Sprintf(" %s took the most: %s.", top.Name, money.Format(top.SpentMinor, currency))
	}
	return body
}
