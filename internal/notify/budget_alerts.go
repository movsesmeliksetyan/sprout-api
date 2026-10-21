package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// The budget thresholds, in percent of a category's monthly budget, that
// raise an alert.
const (
	thresholdNear = 80
	thresholdFull = 100
)

// crossing is a category past a threshold it has not been alerted about.
type crossing struct {
	categoryID  uuid.UUID
	name        string
	spentMinor  int64
	budgetMinor int64
	threshold   int16
}

// CheckBudgets alerts the user about the categories that have reached 80 %
// or 100 % of their budget this month, in the user's timezone, without an
// alert for it yet (PRD BE-57): the work of the budget_check job. A
// category that went past both at once is told only that it is over. Each
// threshold is told once per category and month, so deleting an expense
// and adding it again does not alert again. For an import (importID set)
// several categories make one combined alert. It returns how many
// notifications it sent.
func (s *Service) CheckBudgets(ctx context.Context, userID uuid.UUID, importID *uuid.UUID) (int, error) {
	sent := 0
	err := db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		// Holding the user's row keeps two checks from telling the same
		// threshold.
		user, err := q.GetUserForUpdate(ctx, userID)
		if err != nil {
			return fmt.Errorf("notify: lock user: %w", err)
		}
		loc, err := time.LoadLocation(user.Timezone)
		if err != nil {
			return fmt.Errorf("notify: timezone %q: %w", user.Timezone, err)
		}
		month, err := period.Resolve(period.Month, 0, loc, s.now())
		if err != nil {
			return fmt.Errorf("notify: month: %w", err)
		}
		crossings, err := s.crossings(ctx, q, userID, month)
		if err != nil || len(crossings) == 0 {
			return err
		}
		for _, c := range crossings {
			if err := q.RecordBudgetAlert(ctx, db.RecordBudgetAlertParams{
				UserID: userID, CategoryID: c.categoryID, Month: month.Start, Threshold: c.threshold,
			}); err != nil {
				return fmt.Errorf("notify: record alert: %w", err)
			}
		}
		var alerts []Notification
		if importID != nil && len(crossings) > 1 {
			alerts = []Notification{combinedAlert(crossings, *importID)}
		} else {
			for _, c := range crossings {
				alerts = append(alerts, budgetAlert(c, user.Currency, month.Start))
			}
		}
		for _, n := range alerts {
			created, err := s.SendTx(ctx, tx, userID, n)
			if err != nil {
				return err
			}
			if created {
				sent++
			}
		}
		return nil
	})
	return sent, err
}

// crossings lists the categories with a budget whose spending this month has
// reached a threshold higher than any they were told about.
func (s *Service) crossings(ctx context.Context, q *db.Queries, userID uuid.UUID, month period.Range) ([]crossing, error) {
	spending, err := q.CategorySpending(ctx, db.CategorySpendingParams{
		UserID: userID, FromDate: month.Start, ToDate: month.End, PreviousFromDate: month.Start,
	})
	if err != nil {
		return nil, fmt.Errorf("notify: category spending: %w", err)
	}
	told, err := q.BudgetAlertThresholds(ctx, db.BudgetAlertThresholdsParams{UserID: userID, Month: month.Start})
	if err != nil {
		return nil, fmt.Errorf("notify: alerts told: %w", err)
	}
	highest := make(map[uuid.UUID]int16, len(told))
	for _, t := range told {
		highest[t.CategoryID] = t.Threshold
	}
	var out []crossing
	for _, c := range spending {
		budget := c.MonthlyBudgetMinor
		if budget <= 0 {
			continue
		}
		var threshold int16
		switch {
		case c.SpentMinor >= budget:
			threshold = thresholdFull
		case c.SpentMinor*100 >= budget*thresholdNear:
			threshold = thresholdNear
		default:
			continue
		}
		if highest[c.CategoryID] >= threshold {
			continue
		}
		out = append(out, crossing{categoryID: c.CategoryID, name: c.Name, spentMinor: c.SpentMinor, budgetMinor: budget, threshold: threshold})
	}
	return out, nil
}

// budgetAlert is the notification for one category.
func budgetAlert(c crossing, currency string, month time.Time) Notification {
	limit := money.Format(c.budgetMinor, currency)
	n := Notification{
		Kind:     KindBudgetAlert,
		DeepLink: "sprout://category/" + c.categoryID.String(),
		DedupKey: fmt.Sprintf("budget:%s:%s:%d", c.categoryID, month.Format("2006-01"), c.threshold),
	}
	switch {
	case c.threshold == thresholdNear:
		used, _ := money.Percent(c.spentMinor, c.budgetMinor)
		n.Title = fmt.Sprintf("%s is at %d%% of its budget", c.name, used)
		n.Body = fmt.Sprintf("%s left of your %s limit this month.", money.Format(c.budgetMinor-c.spentMinor, currency), limit)
	case c.spentMinor > c.budgetMinor:
		n.Title = c.name + " is over budget"
		n.Body = fmt.Sprintf("You're %s over your %s limit this month.", money.Format(c.spentMinor-c.budgetMinor, currency), limit)
	default:
		n.Title = c.name + " has reached its budget"
		n.Body = fmt.Sprintf("You've used all of your %s limit this month.", limit)
	}
	return n
}

// combinedAlert is an import's one notification for several categories.
func combinedAlert(crossings []crossing, importID uuid.UUID) Notification {
	var over, near []string
	for _, c := range crossings {
		if c.threshold == thresholdFull {
			over = append(over, c.name)
		} else {
			near = append(near, c.name)
		}
	}
	var parts []string
	if len(over) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s reached %s", list(over), verb(len(over), "has", "have"), verb(len(over), "its budget", "their budgets")))
	}
	if len(near) > 0 {
		parts = append(parts, fmt.Sprintf("%s %s close to %s", list(near), verb(len(near), "is", "are"), verb(len(near), "its budget", "their budgets")))
	}
	body := strings.Join(parts, "; ") + "."
	return Notification{
		Kind:     KindBudgetAlert,
		Title:    fmt.Sprintf("%d budgets need a look", len(crossings)),
		Body:     strings.ToUpper(body[:1]) + body[1:],
		DeepLink: "sprout://stats",
		DedupKey: "budget:import:" + importID.String(),
	}
}

// list joins names as "Food, Bills and Travel".
func list(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
