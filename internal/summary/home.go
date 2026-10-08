package summary

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// recentLimit is how many of the newest transactions Home shows.
const recentLimit = 10

// Service computes the aggregated views of a user's ledger.
type Service struct {
	db     db.DBTX
	ledger *transactions.Service
	now    func() time.Time
}

// Option customises a Service.
type Option func(*Service)

// WithClock sets the time that decides which period is the current one.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService returns a Service reading from database, with ledger for the
// transaction lists its views include.
func NewService(database db.DBTX, ledger *transactions.Service, opts ...Option) *Service {
	s := &Service{db: database, ledger: ledger, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Home is everything the Home screen shows.
type Home struct {
	// BalanceMinor is what the user has: the starting balance and every
	// transaction since. It may be negative.
	BalanceMinor int64
	Month        Month
	// Recent holds the newest transactions and the totals of their days.
	Recent                 transactions.Page
	HasUnreadNotifications bool
}

// Month is the spending of the current calendar month.
type Month struct {
	Range              period.Range
	SpentMinor         int64
	PreviousSpentMinor int64
	// DeltaMinor is this month minus the previous one.
	DeltaMinor int64
	// CategoriesCount is how many active categories the user has.
	CategoriesCount int
	// Segments are the categories with spend this month, largest first.
	Segments []Segment
}

// Segment is one category's part of a period's spending.
type Segment struct {
	CategoryID uuid.UUID
	SpentMinor int64
	// SharePct is the part of the spending in whole percent; the segments
	// of a period add up to 100.
	SharePct int
}

// Home returns the user's balance, their spending this month and their
// newest transactions.
func (s *Service) Home(ctx context.Context, user db.User) (Home, error) {
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return Home{}, fmt.Errorf("summary: timezone %q: %w", user.Timezone, err)
	}
	current, err := period.Resolve(period.Month, 0, loc, s.now())
	if err != nil {
		return Home{}, fmt.Errorf("summary: current month: %w", err)
	}
	q := db.New(s.db)

	// Goal contributions join the balance once goals exist (BE-26).
	net, err := q.LedgerNet(ctx, user.ID)
	if err != nil {
		return Home{}, fmt.Errorf("summary: ledger net: %w", err)
	}
	month, err := s.month(ctx, q, user.ID, current)
	if err != nil {
		return Home{}, err
	}
	limit := recentLimit
	recent, err := s.ledger.List(ctx, user.ID, transactions.ListParams{Limit: &limit})
	if err != nil {
		return Home{}, err
	}
	recent.Next = nil

	return Home{
		BalanceMinor: user.StartingBalanceMinor + net,
		Month:        month,
		Recent:       recent,
		// Until there is an inbox (BE-56) nothing is unread.
		HasUnreadNotifications: false,
	}, nil
}

// month returns the spending of the month r against the one before it.
func (s *Service) month(ctx context.Context, q *db.Queries, userID uuid.UUID, r period.Range) (Month, error) {
	byCategory, err := q.SpentByCategory(ctx, db.SpentByCategoryParams{UserID: userID, FromDate: r.Start, ToDate: r.End})
	if err != nil {
		return Month{}, fmt.Errorf("summary: spent by category: %w", err)
	}
	previous := r.Previous()
	previousSpent, err := q.SpentBetween(ctx, db.SpentBetweenParams{UserID: userID, FromDate: previous.Start, ToDate: previous.End})
	if err != nil {
		return Month{}, fmt.Errorf("summary: previous spent: %w", err)
	}
	categories, err := q.CountActiveCategories(ctx, userID)
	if err != nil {
		return Month{}, fmt.Errorf("summary: count categories: %w", err)
	}

	var spent int64
	amounts := make([]int64, 0, len(byCategory))
	for _, row := range byCategory {
		spent += row.SpentMinor
		amounts = append(amounts, row.SpentMinor)
	}
	shares := money.Shares(amounts)
	segments := make([]Segment, 0, len(byCategory))
	for i, row := range byCategory {
		segments = append(segments, Segment{CategoryID: row.CategoryID, SpentMinor: row.SpentMinor, SharePct: shares[i]})
	}

	return Month{
		Range:              r,
		SpentMinor:         spent,
		PreviousSpentMinor: previousSpent,
		DeltaMinor:         spent - previousSpent,
		CategoriesCount:    int(categories),
		Segments:           segments,
	}, nil
}
