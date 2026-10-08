package categories

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

const (
	// MaxActive is how many categories a user can have that are not archived.
	MaxActive = 30

	maxNameLength = 24
	maxShade      = 6
	// shades is the length of the client's slate ramp.
	shades = maxShade + 1
	// maxBudgetMinor bounds a monthly budget, far below where sums of
	// amounts could overflow.
	maxBudgetMinor = 1_000_000_000_000
)

// Database is what the service needs from the pool: queries, and
// transactions for the writes that must happen together.
type Database interface {
	db.DBTX
	db.Beginner
}

// UsageCheck reports whether any transaction points at the category. q reads
// through the transaction that is about to delete it.
type UsageCheck func(ctx context.Context, q db.Querier, userID, categoryID uuid.UUID) (bool, error)

// Service holds the business logic for categories and their budgets.
type Service struct {
	db     Database
	isUsed UsageCheck
}

// Option customises a Service.
type Option func(*Service)

// WithUsageCheck sets how the service learns that a category has
// transactions, which is when it can no longer be deleted. Without it no
// category has any.
func WithUsageCheck(check UsageCheck) Option {
	return func(s *Service) { s.isUsed = check }
}

// NewService returns a Service working on database.
func NewService(database Database, opts ...Option) *Service {
	s := &Service{
		db:     database,
		isUsed: func(context.Context, db.Querier, uuid.UUID, uuid.UUID) (bool, error) { return false, nil },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// List returns the user's active categories in display order, followed by
// the archived ones when includeArchived is set.
func (s *Service) List(ctx context.Context, userID uuid.UUID, includeArchived bool) ([]db.Category, error) {
	categories, err := db.New(s.db).ListCategories(ctx, db.ListCategoriesParams{
		UserID:          userID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, fmt.Errorf("categories: list: %w", err)
	}
	return categories, nil
}

// CreateParams describes a new category. A nil Shade picks the next one on
// the ramp; a nil budget is zero.
type CreateParams struct {
	Name               string
	Icon               string
	Shade              *int
	MonthlyBudgetMinor *int64
}

// Create adds a category at the end of the user's list. Invalid values are
// reported together in an *httpx.ValidationError.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, params CreateParams) (db.Category, error) {
	fields := map[string]string{}
	name := checkName(fields, params.Name)
	checkIcon(fields, params.Icon)
	checkShade(fields, params.Shade)
	checkBudget(fields, "monthly_budget_minor", params.MonthlyBudgetMinor)
	if len(fields) > 0 {
		return db.Category{}, &httpx.ValidationError{Fields: fields}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return db.Category{}, fmt.Errorf("categories: new id: %w", err)
	}

	var created db.Category
	err = s.write(ctx, userID, func(q *db.Queries) error {
		if err := checkRoom(ctx, q, userID); err != nil {
			return err
		}
		if err := checkNameFree(ctx, q, userID, name, id); err != nil {
			return err
		}
		sortOrder, err := q.NextCategorySortOrder(ctx, userID)
		if err != nil {
			return fmt.Errorf("categories: next sort order: %w", err)
		}

		shade := int16(sortOrder % shades)
		if params.Shade != nil {
			shade = int16(*params.Shade)
		}
		var budget int64
		if params.MonthlyBudgetMinor != nil {
			budget = *params.MonthlyBudgetMinor
		}
		created, err = q.CreateCategory(ctx, db.CreateCategoryParams{
			ID:                 id,
			UserID:             userID,
			Name:               name,
			Icon:               params.Icon,
			Shade:              shade,
			SortOrder:          sortOrder,
			MonthlyBudgetMinor: budget,
			CategoryType:       InferType(name, params.Icon),
		})
		if err != nil {
			return fmt.Errorf("categories: create: %w", err)
		}
		return nil
	})
	if err != nil {
		return db.Category{}, err
	}
	return created, nil
}

// Patch is a partial update of a category. A nil field is left as it is.
type Patch struct {
	Name               *string
	Icon               *string
	Shade              *int
	MonthlyBudgetMinor *int64
	// Archived hides the category (true) or brings it back (false).
	Archived *bool
}

// Update applies patch to the user's category and returns the result. A
// category that comes back from the archive goes to the end of the list and
// must fit the limit and not clash with an active name.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, patch Patch) (db.Category, error) {
	fields := map[string]string{}
	params := db.UpdateCategoryParams{
		ID:                 id,
		UserID:             userID,
		Icon:               patch.Icon,
		MonthlyBudgetMinor: patch.MonthlyBudgetMinor,
		Archived:           patch.Archived,
	}
	if patch.Name != nil {
		name := checkName(fields, *patch.Name)
		params.Name = &name
	}
	if patch.Icon != nil {
		checkIcon(fields, *patch.Icon)
	}
	if patch.Shade != nil {
		checkShade(fields, patch.Shade)
		shade := int16(*patch.Shade)
		params.Shade = &shade
	}
	checkBudget(fields, "monthly_budget_minor", patch.MonthlyBudgetMinor)
	if len(fields) > 0 {
		return db.Category{}, &httpx.ValidationError{Fields: fields}
	}

	var updated db.Category
	err := s.write(ctx, userID, func(q *db.Queries) error {
		current, err := q.GetCategory(ctx, db.GetCategoryParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("categories: get: %w", err)
		}

		wasActive := current.ArchivedAt == nil
		active := wasActive
		if patch.Archived != nil {
			active = !*patch.Archived
		}
		restored := active && !wasActive
		name := current.Name
		if params.Name != nil {
			name = *params.Name
		}

		if restored {
			if err := checkRoom(ctx, q, userID); err != nil {
				return err
			}
			sortOrder, err := q.NextCategorySortOrder(ctx, userID)
			if err != nil {
				return fmt.Errorf("categories: next sort order: %w", err)
			}
			params.SortOrder = &sortOrder
		}
		// Only active names must differ, so the check waits until the
		// category is, or becomes, active.
		if active && (restored || !strings.EqualFold(name, current.Name)) {
			if err := checkNameFree(ctx, q, userID, name, id); err != nil {
				return err
			}
		}
		if current.CategoryType == nil {
			icon := current.Icon
			if patch.Icon != nil {
				icon = *patch.Icon
			}
			params.CategoryType = InferType(name, icon)
		}

		updated, err = q.UpdateCategory(ctx, params)
		if err != nil {
			return fmt.Errorf("categories: update: %w", err)
		}
		return nil
	})
	if err != nil {
		return db.Category{}, err
	}
	return updated, nil
}

// Delete removes the user's category. One that has transactions cannot go:
// the answer is a conflict, and the client archives it instead.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.write(ctx, userID, func(q *db.Queries) error {
		if _, err := q.GetCategory(ctx, db.GetCategoryParams{ID: id, UserID: userID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.ErrNotFound
			}
			return fmt.Errorf("categories: get: %w", err)
		}
		used, err := s.isUsed(ctx, q, userID, id)
		if err != nil {
			return fmt.Errorf("categories: usage check: %w", err)
		}
		if used {
			return httpx.WithMessage(httpx.ErrConflict,
				"This category has transactions and cannot be deleted. Archive it instead.")
		}
		if _, err := q.DeleteCategory(ctx, db.DeleteCategoryParams{ID: id, UserID: userID}); err != nil {
			return fmt.Errorf("categories: delete: %w", err)
		}
		return nil
	})
}

// Reorder puts the user's active categories in the order of ids, which must
// name each of them exactly once, and returns them in that order.
func (s *Service) Reorder(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]db.Category, error) {
	var reordered []db.Category
	err := s.write(ctx, userID, func(q *db.Queries) error {
		active, err := q.ListCategories(ctx, db.ListCategoriesParams{UserID: userID})
		if err != nil {
			return fmt.Errorf("categories: list: %w", err)
		}
		if !sameSet(active, ids) {
			return &httpx.ValidationError{Fields: map[string]string{
				"ids": "must list every active category exactly once",
			}}
		}
		if err := q.ReorderCategories(ctx, db.ReorderCategoriesParams{UserID: userID, Ids: ids}); err != nil {
			return fmt.Errorf("categories: reorder: %w", err)
		}
		reordered, err = q.ListCategories(ctx, db.ListCategoriesParams{UserID: userID})
		if err != nil {
			return fmt.Errorf("categories: list: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reordered, nil
}

// sameSet reports whether ids names each of categories exactly once.
func sameSet(categories []db.Category, ids []uuid.UUID) bool {
	if len(ids) != len(categories) {
		return false
	}
	remaining := make(map[uuid.UUID]bool, len(categories))
	for _, category := range categories {
		remaining[category.ID] = true
	}
	for _, id := range ids {
		if !remaining[id] {
			return false
		}
		delete(remaining, id)
	}
	return true
}

// write runs fn in a transaction that holds the user's row, so that one
// user's category changes happen one at a time: the limit, the names and the
// order are then checked against a list that cannot move.
func (s *Service) write(ctx context.Context, userID uuid.UUID, fn func(q *db.Queries) error) error {
	return db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, err := q.GetUserForUpdate(ctx, userID); err != nil {
			return fmt.Errorf("categories: lock user: %w", err)
		}
		return fn(q)
	})
}

// checkRoom refuses another active category once the user has MaxActive.
func checkRoom(ctx context.Context, q *db.Queries, userID uuid.UUID) error {
	count, err := q.CountActiveCategories(ctx, userID)
	if err != nil {
		return fmt.Errorf("categories: count: %w", err)
	}
	if count >= MaxActive {
		return httpx.WithMessage(httpx.ErrConflict,
			fmt.Sprintf("You can have at most %d categories. Archive or delete one first.", MaxActive))
	}
	return nil
}

// checkNameFree refuses a name another active category of the user has.
func checkNameFree(ctx context.Context, q *db.Queries, userID uuid.UUID, name string, exceptID uuid.UUID) error {
	taken, err := q.CategoryNameTaken(ctx, db.CategoryNameTakenParams{UserID: userID, Name: name, ExceptID: exceptID})
	if err != nil {
		return fmt.Errorf("categories: name check: %w", err)
	}
	if taken {
		return &httpx.ValidationError{Fields: map[string]string{"name": "is already used by another category"}}
	}
	return nil
}

// checkName returns name as it is stored, noting in fields what is wrong
// with it.
func checkName(fields map[string]string, name string) string {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		fields["name"] = "must not be empty"
	case utf8.RuneCountInString(name) > maxNameLength:
		fields["name"] = fmt.Sprintf("must be at most %d characters", maxNameLength)
	}
	return name
}

func checkIcon(fields map[string]string, icon string) {
	if !httpx.CategoryIcon(icon).Valid() {
		fields["icon"] = "is not a known icon"
	}
}

func checkShade(fields map[string]string, shade *int) {
	if shade != nil && (*shade < 0 || *shade > maxShade) {
		fields["shade"] = fmt.Sprintf("must be between 0 and %d", maxShade)
	}
}

func checkBudget(fields map[string]string, field string, budget *int64) {
	switch {
	case budget == nil:
	case *budget < 0:
		fields[field] = "must not be negative"
	case *budget > maxBudgetMinor:
		fields[field] = "is out of range"
	}
}
