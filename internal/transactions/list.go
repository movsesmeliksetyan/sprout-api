package transactions

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

const (
	// DefaultLimit is the page size when the request names none.
	DefaultLimit = 50
	// MaxLimit is the largest page a request can ask for.
	MaxLimit = 200

	maxQueryLength = 100
)

// ListParams selects a page of transactions. A nil filter is not applied;
// an empty Query searches for nothing. A nil Limit is DefaultLimit, and a
// nil After starts at the newest transaction.
type ListParams struct {
	// From and To are calendar days, both included.
	From       *time.Time
	To         *time.Time
	CategoryID *uuid.UUID
	Kind       *Kind
	// Query is text the merchant or the note must contain, in any case.
	Query string
	Limit *int
	// After is the last transaction of the previous page.
	After *httpx.Cursor
}

// DayTotal is the whole of one calendar day under a list's filters, however
// much of the day the page holds.
type DayTotal struct {
	Date time.Time
	// NetMinor is income minus expense.
	NetMinor int64
	Count    int
}

// Page is one page of a list, newest first.
type Page struct {
	Items []db.Transaction
	// Days has one total for every date in Items, newest first.
	Days []DayTotal
	// Next is where the following page starts; nil on the last page.
	Next *httpx.Cursor
}

// List returns a page of the user's transactions ordered by local date and
// id, newest first, with the totals of the days on it.
func (s *Service) List(ctx context.Context, userID uuid.UUID, params ListParams) (Page, error) {
	limit := DefaultLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > MaxLimit {
		return Page{}, httpx.WithMessage(httpx.ErrBadRequest,
			fmt.Sprintf("The limit must be between 1 and %d.", MaxLimit))
	}
	var kind *string
	if params.Kind != nil {
		if *params.Kind != KindExpense && *params.Kind != KindIncome {
			return Page{}, httpx.WithMessage(httpx.ErrBadRequest, "The kind must be expense or income.")
		}
		value := string(*params.Kind)
		kind = &value
	}
	query := strings.TrimSpace(params.Query)
	if utf8.RuneCountInString(query) > maxQueryLength {
		return Page{}, httpx.WithMessage(httpx.ErrBadRequest,
			fmt.Sprintf("The search text must be at most %d characters.", maxQueryLength))
	}
	var pattern *string
	if query != "" {
		value := containsPattern(query)
		pattern = &value
	}

	list := db.ListTransactionsParams{
		UserID:     userID,
		FromDate:   params.From,
		ToDate:     params.To,
		CategoryID: params.CategoryID,
		Kind:       kind,
		Pattern:    pattern,
		// One row more than the page tells whether another page follows.
		RowLimit: int32(limit) + 1,
	}
	if params.After != nil {
		list.CursorDate = &params.After.LocalDate
		list.CursorID = &params.After.ID
	}
	q := db.New(s.db)
	items, err := q.ListTransactions(ctx, list)
	if err != nil {
		return Page{}, fmt.Errorf("transactions: list: %w", err)
	}

	page := Page{Items: items, Days: []DayTotal{}}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &httpx.Cursor{LocalDate: last.LocalDate, ID: last.ID}
	}
	if len(page.Items) == 0 {
		return page, nil
	}

	// Items are in date order, so a day's rows are next to each other.
	var dates []time.Time
	for _, item := range page.Items {
		if len(dates) == 0 || !dates[len(dates)-1].Equal(item.LocalDate) {
			dates = append(dates, item.LocalDate)
		}
	}
	totals, err := q.ListDayTotals(ctx, db.ListDayTotalsParams{
		UserID:     userID,
		Dates:      dates,
		FromDate:   params.From,
		ToDate:     params.To,
		CategoryID: params.CategoryID,
		Kind:       kind,
		Pattern:    pattern,
	})
	if err != nil {
		return Page{}, fmt.Errorf("transactions: day totals: %w", err)
	}
	for _, total := range totals {
		page.Days = append(page.Days, DayTotal{Date: total.LocalDate, NetMinor: total.NetMinor, Count: int(total.Count)})
	}
	return page, nil
}

// likeEscaper makes the characters LIKE gives a meaning to stand for
// themselves.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// containsPattern returns the LIKE pattern for "contains text".
func containsPattern(text string) string {
	return "%" + likeEscaper.Replace(text) + "%"
}
