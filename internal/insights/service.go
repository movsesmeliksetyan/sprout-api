package insights

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/goals"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
	"github.com/movsesmeliksetyan/sprout-api/internal/summary"
)

// Database is what the service needs from the pool: queries, and
// transactions to replace a period's cards.
type Database interface {
	db.DBTX
	db.Beginner
}

// Service computes a period's insights and keeps them until the ledger
// changes.
type Service struct {
	db     Database
	goals  *goals.Service
	engine *Engine
	now    func() time.Time
	// computed counts computations, for tests that a read was served from
	// the table.
	computed func()
}

// Option configures a Service.
type Option func(*Service)

// WithClock makes the service read the time from now.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithComputeHook calls fn every time a period's cards are computed rather
// than read back.
func WithComputeHook(fn func()) Option {
	return func(s *Service) { s.computed = fn }
}

// NewService returns a service over the database, reading goals through
// goalService.
func NewService(database Database, goalService *goals.Service, opts ...Option) *Service {
	s := &Service{db: database, goals: goalService, engine: NewEngine(), now: time.Now, computed: func() {}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// List returns the user's cards for the period of the given kind, offset
// periods back, most relevant first (PRD BE-54). They are computed on read
// and kept: a read serves the stored cards while the user's ledger version
// and calendar day are the ones they were computed at, and computes them
// again otherwise.
func (s *Service) List(ctx context.Context, user db.User, kind string, offset *int) ([]db.Insight, error) {
	r, today, err := s.resolve(user, kind, offset)
	if err != nil {
		return nil, err
	}
	q := db.New(s.db)
	if cards, fresh, err := s.stored(ctx, q, user.ID, r, today); err != nil || fresh {
		return cards, err
	}
	var cards []db.Insight
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		cards, err = s.refresh(ctx, db.New(tx), user, r, today)
		return err
	})
	return cards, err
}

// Refresh computes the user's cards for the current month unless the
// stored ones are fresh: the nightly job's work.
func (s *Service) Refresh(ctx context.Context, user db.User) error {
	_, err := s.List(ctx, user, string(period.Month), nil)
	return err
}

// stored returns the period's stored cards, and whether they are fresh.
func (s *Service) stored(ctx context.Context, q *db.Queries, userID uuid.UUID, r period.Range, today time.Time) ([]db.Insight, bool, error) {
	version, err := q.GetLedgerVersion(ctx, userID)
	if err != nil {
		return nil, false, fmt.Errorf("insights: ledger version: %w", err)
	}
	run, err := q.GetInsightRun(ctx, db.GetInsightRunParams{UserID: userID, Period: string(r.Kind), PeriodStart: r.Start})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("insights: run: %w", err)
	case run.LedgerVersion != version || !run.ComputedOn.Equal(today):
		return nil, false, nil
	}
	cards, err := q.ListInsights(ctx, db.ListInsightsParams{UserID: userID, Period: string(r.Kind), PeriodStart: r.Start})
	if err != nil {
		return nil, false, fmt.Errorf("insights: list: %w", err)
	}
	return cards, true, nil
}

// refresh computes and stores the period's cards, holding the user's row
// so that two reads do not compute them at once; the one that waited
// serves what the other stored.
func (s *Service) refresh(ctx context.Context, q *db.Queries, user db.User, r period.Range, today time.Time) ([]db.Insight, error) {
	user, err := q.GetUserForUpdate(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("insights: lock user: %w", err)
	}
	if cards, fresh, err := s.stored(ctx, q, user.ID, r, today); err != nil || fresh {
		return cards, err
	}
	snapshot, err := s.snapshot(ctx, q, user, r, today)
	if err != nil {
		return nil, err
	}
	s.computed()
	candidates := s.engine.Evaluate(ctx, snapshot)

	key := db.DeleteInsightsParams{UserID: user.ID, Period: string(r.Kind), PeriodStart: r.Start}
	if err := q.DeleteInsights(ctx, key); err != nil {
		return nil, fmt.Errorf("insights: clear: %w", err)
	}
	for i, c := range candidates {
		if err := q.CreateInsight(ctx, db.CreateInsightParams{
			ID: uuid.Must(uuid.NewV7()), UserID: user.ID, Period: string(r.Kind), PeriodStart: r.Start,
			Position: int32(i), Kind: string(c.Kind), Tone: string(c.Tone), Icon: string(c.Icon),
			Title: c.Title, Body: c.Body, CategoryID: c.CategoryID, GoalID: c.GoalID, Score: int32(c.Score),
		}); err != nil {
			return nil, fmt.Errorf("insights: store: %w", err)
		}
	}
	if err := q.SaveInsightRun(ctx, db.SaveInsightRunParams{
		UserID: user.ID, Period: string(r.Kind), PeriodStart: r.Start, LedgerVersion: user.LedgerVersion, ComputedOn: today,
	}); err != nil {
		return nil, fmt.Errorf("insights: save run: %w", err)
	}
	cards, err := q.ListInsights(ctx, db.ListInsightsParams(key))
	if err != nil {
		return nil, fmt.Errorf("insights: list: %w", err)
	}
	return cards, nil
}

// snapshot reads what the rules look at.
func (s *Service) snapshot(ctx context.Context, q *db.Queries, user db.User, r period.Range, today time.Time) (Snapshot, error) {
	snap := Snapshot{Range: r, Today: today, Currency: user.Currency}
	previous := r.Previous()

	days, err := q.ExpensesByDay(ctx, db.ExpensesByDayParams{UserID: user.ID, FromDate: r.Start, ToDate: r.End})
	if err != nil {
		return Snapshot{}, fmt.Errorf("insights: expenses by day: %w", err)
	}
	for _, d := range days {
		snap.TotalSpentMinor += d.SpentMinor
		snap.TxnCount += int(d.TxnCount)
		snap.Weekdays[d.LocalDate.Weekday()] += d.SpentMinor
	}
	snap.PreviousTotalMinor, err = q.SpentBetween(ctx, db.SpentBetweenParams{UserID: user.ID, FromDate: previous.Start, ToDate: previous.End})
	if err != nil {
		return Snapshot{}, fmt.Errorf("insights: previous spent: %w", err)
	}

	categories, err := q.CategorySpending(ctx, db.CategorySpendingParams{
		UserID: user.ID, FromDate: r.Start, ToDate: r.End, PreviousFromDate: previous.Start,
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("insights: category spending: %w", err)
	}
	largest, err := q.LargestExpenses(ctx, db.LargestExpensesParams{UserID: user.ID, FromDate: r.Start, ToDate: r.End})
	if err != nil {
		return Snapshot{}, fmt.Errorf("insights: largest expenses: %w", err)
	}
	byCategory := make(map[uuid.UUID]*Expense, len(largest))
	for _, l := range largest {
		byCategory[l.CategoryID] = &Expense{AmountMinor: l.AmountMinor, Merchant: l.Merchant, LocalDate: l.LocalDate}
	}
	for _, c := range categories {
		snap.Categories = append(snap.Categories, Category{
			ID: c.CategoryID, Name: c.Name,
			SpentMinor: c.SpentMinor, PreviousSpentMinor: c.PreviousSpentMinor,
			BudgetMinor: period.ScaleBudget(c.MonthlyBudgetMinor, r.Kind),
			TxnCount:    int(c.TxnCount), Largest: byCategory[c.CategoryID],
		})
	}

	list, err := s.goals.List(ctx, user)
	if err != nil {
		return Snapshot{}, fmt.Errorf("insights: goals: %w", err)
	}
	for _, g := range list.Items {
		if g.Status != string(goals.StatusActive) {
			continue
		}
		snap.Goals = append(snap.Goals, Goal{
			ID: g.ID, Title: g.Title, TargetMinor: g.TargetMinor, SavedMinor: g.SavedMinor,
			MonthlyPaceMinor: g.MonthlyPaceMinor, EtaMonth: g.EtaMonth,
		})
	}
	return snap, nil
}

// resolve returns the period a request names and today's calendar day,
// both in the user's timezone.
func (s *Service) resolve(user db.User, kind string, offset *int) (period.Range, time.Time, error) {
	parsed, err := period.ParseKind(kind)
	if err != nil {
		return period.Range{}, time.Time{}, httpx.WithMessage(httpx.ErrBadRequest, "The period must be week, month or year.")
	}
	back := 0
	if offset != nil {
		back = *offset
	}
	if back < 0 || back > summary.MaxOffset {
		return period.Range{}, time.Time{}, httpx.WithMessage(httpx.ErrBadRequest,
			fmt.Sprintf("The offset must be between 0 and %d.", summary.MaxOffset))
	}
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return period.Range{}, time.Time{}, fmt.Errorf("insights: timezone %q: %w", user.Timezone, err)
	}
	now := s.now()
	r, err := period.Resolve(parsed, back, loc, now)
	if err != nil {
		return period.Range{}, time.Time{}, fmt.Errorf("insights: resolve period: %w", err)
	}
	return r, period.LocalDate(now, loc), nil
}
