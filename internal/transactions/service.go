package transactions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
)

// Kind is the direction of a transaction.
type Kind string

// The kinds of transaction (contract §1.3).
const (
	KindExpense Kind = "expense"
	KindIncome  Kind = "income"
)

// SourceManual marks a transaction the user entered by hand.
const SourceManual = "manual"

const (
	maxMerchantLength = 80
	maxNoteLength     = 140
	// maxAmountMinor bounds one amount, far below where sums of amounts
	// could overflow.
	maxAmountMinor = 1_000_000_000_000
)

// Database is what the service needs from the pool: queries, and
// transactions for the writes that must happen together.
type Database interface {
	db.DBTX
	db.Beginner
}

// MerchantRuleHook runs when the user files a merchant under a category by
// hand, inside the database transaction that saves it: q reads and writes
// through that transaction, and an error undoes the save.
type MerchantRuleHook func(ctx context.Context, q db.Querier, userID uuid.UUID, merchantKey string, categoryID uuid.UUID) error

// Service holds the business logic for the ledger.
type Service struct {
	db             Database
	now            func() time.Time
	onMerchantRule MerchantRuleHook
}

// Option customises a Service.
type Option func(*Service)

// WithClock sets the time a transaction without one is given.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithMerchantRuleHook sets what happens when a manual expense with a
// merchant is saved. Without it nothing does.
func WithMerchantRuleHook(hook MerchantRuleHook) Option {
	return func(s *Service) { s.onMerchantRule = hook }
}

// NewService returns a Service working on database.
func NewService(database Database, opts ...Option) *Service {
	s := &Service{
		db:             database,
		now:            time.Now,
		onMerchantRule: func(context.Context, db.Querier, uuid.UUID, string, uuid.UUID) error { return nil },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// HasAny reports whether the user has any transactions.
func HasAny(ctx context.Context, q db.Querier, userID uuid.UUID) (bool, error) {
	return q.UserHasTransactions(ctx, userID)
}

// CategoryInUse reports whether any of the user's transactions points at
// the category.
func CategoryInUse(ctx context.Context, q db.Querier, userID, categoryID uuid.UUID) (bool, error) {
	return q.CategoryHasTransactions(ctx, db.CategoryHasTransactionsParams{UserID: userID, CategoryID: &categoryID})
}

// CreateParams describes a new transaction. A nil OccurredAt is now.
type CreateParams struct {
	Kind        Kind
	AmountMinor int64
	CategoryID  *uuid.UUID
	Merchant    *string
	Note        *string
	OccurredAt  *time.Time
}

// Create adds a manual transaction to the user's ledger. Invalid values are
// reported together in an *httpx.ValidationError.
func (s *Service) Create(ctx context.Context, user db.User, params CreateParams) (db.Transaction, error) {
	loc, err := location(user)
	if err != nil {
		return db.Transaction{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return db.Transaction{}, fmt.Errorf("transactions: new id: %w", err)
	}

	fields := map[string]string{}
	checkKind(fields, params.Kind)
	checkAmount(fields, params.AmountMinor)
	merchant := checkText(fields, "merchant", params.Merchant, maxMerchantLength)
	note := checkText(fields, "note", params.Note, maxNoteLength)
	checkCategoryFitsKind(fields, params.Kind, params.CategoryID)

	occurredAt := s.now()
	if params.OccurredAt != nil {
		occurredAt = *params.OccurredAt
	}
	localDate := period.LocalDate(occurredAt, loc)
	merchantKey := key(merchant)

	var created db.Transaction
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		if _, bad := fields["category_id"]; !bad && params.CategoryID != nil {
			if err := checkCategoryActive(ctx, q, fields, user.ID, *params.CategoryID); err != nil {
				return err
			}
		}
		if len(fields) > 0 {
			return &httpx.ValidationError{Fields: fields}
		}

		created, err = q.CreateTransaction(ctx, db.CreateTransactionParams{
			ID:          id,
			UserID:      user.ID,
			Kind:        string(params.Kind),
			AmountMinor: params.AmountMinor,
			CategoryID:  params.CategoryID,
			Merchant:    merchant,
			MerchantKey: merchantKey,
			Note:        note,
			OccurredAt:  occurredAt,
			LocalDate:   localDate,
			Source:      SourceManual,
			DedupHash:   DedupHash(localDate, params.AmountMinor, params.Kind, merchantKey),
		})
		if err != nil {
			return fmt.Errorf("transactions: create: %w", err)
		}
		return s.merchantRule(ctx, q, created)
	})
	if err != nil {
		return db.Transaction{}, err
	}
	return created, nil
}

// Get returns the user's transaction.
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (db.Transaction, error) {
	transaction, err := db.New(s.db).GetTransaction(ctx, db.GetTransactionParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Transaction{}, httpx.ErrNotFound
	}
	if err != nil {
		return db.Transaction{}, fmt.Errorf("transactions: get: %w", err)
	}
	return transaction, nil
}

// Optional is a field of a partial update that can also be cleared: left as
// it is unless Set, and cleared when Set with a nil Value.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// Patch is a partial update of a transaction. A nil or unset field is left
// as it is.
type Patch struct {
	Kind        *Kind
	AmountMinor *int64
	CategoryID  Optional[uuid.UUID]
	Merchant    Optional[string]
	Note        Optional[string]
	OccurredAt  *time.Time
}

// Update applies patch to the user's transaction and returns the result,
// which must be as valid as a new transaction. A transaction that becomes
// income loses its category. The local date follows the time only when the
// time changes, so a user who moved timezone does not move their history.
func (s *Service) Update(ctx context.Context, user db.User, id uuid.UUID, patch Patch) (db.Transaction, error) {
	loc, err := location(user)
	if err != nil {
		return db.Transaction{}, err
	}

	var updated db.Transaction
	err = db.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		q := db.New(tx)
		current, err := q.GetTransactionForUpdate(ctx, db.GetTransactionForUpdateParams{ID: id, UserID: user.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("transactions: get: %w", err)
		}

		fields := map[string]string{}
		params := db.UpdateTransactionParams{
			ID:          id,
			UserID:      user.ID,
			Kind:        current.Kind,
			AmountMinor: current.AmountMinor,
			CategoryID:  current.CategoryID,
			Merchant:    current.Merchant,
			Note:        current.Note,
			OccurredAt:  current.OccurredAt,
			LocalDate:   current.LocalDate,
		}
		if patch.Kind != nil {
			checkKind(fields, *patch.Kind)
			params.Kind = string(*patch.Kind)
		}
		kind := Kind(params.Kind)
		if patch.AmountMinor != nil {
			checkAmount(fields, *patch.AmountMinor)
			params.AmountMinor = *patch.AmountMinor
		}
		if patch.Merchant.Set {
			params.Merchant = checkText(fields, "merchant", patch.Merchant.Value, maxMerchantLength)
		}
		if patch.Note.Set {
			params.Note = checkText(fields, "note", patch.Note.Value, maxNoteLength)
		}
		if patch.OccurredAt != nil && !patch.OccurredAt.Equal(current.OccurredAt) {
			params.OccurredAt = *patch.OccurredAt
			params.LocalDate = period.LocalDate(*patch.OccurredAt, loc)
		}

		switch {
		case patch.CategoryID.Set:
			params.CategoryID = patch.CategoryID.Value
		case kind == KindIncome:
			params.CategoryID = nil
		}
		checkCategoryFitsKind(fields, kind, params.CategoryID)
		// A transaction keeps a category that was archived after it was
		// filed; only a newly chosen one has to be active.
		_, bad := fields["category_id"]
		if !bad && params.CategoryID != nil && !sameID(params.CategoryID, current.CategoryID) {
			if err := checkCategoryActive(ctx, q, fields, user.ID, *params.CategoryID); err != nil {
				return err
			}
		}
		if len(fields) > 0 {
			return &httpx.ValidationError{Fields: fields}
		}

		params.MerchantKey = key(params.Merchant)
		params.DedupHash = DedupHash(params.LocalDate, params.AmountMinor, kind, params.MerchantKey)
		updated, err = q.UpdateTransaction(ctx, params)
		if err != nil {
			return fmt.Errorf("transactions: update: %w", err)
		}
		return s.merchantRule(ctx, q, updated)
	})
	if err != nil {
		return db.Transaction{}, err
	}
	return updated, nil
}

// Delete removes the user's transaction.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	deleted, err := db.New(s.db).DeleteTransaction(ctx, db.DeleteTransactionParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("transactions: delete: %w", err)
	}
	if deleted == 0 {
		return httpx.ErrNotFound
	}
	return nil
}

// merchantRule runs the hook for a saved transaction that files a merchant
// under a category by hand.
func (s *Service) merchantRule(ctx context.Context, q db.Querier, saved db.Transaction) error {
	if saved.Source != SourceManual || saved.CategoryID == nil || saved.MerchantKey == "" {
		return nil
	}
	if err := s.onMerchantRule(ctx, q, saved.UserID, saved.MerchantKey, *saved.CategoryID); err != nil {
		return fmt.Errorf("transactions: merchant rule: %w", err)
	}
	return nil
}

// location returns the timezone the user's calendar days are in.
func location(user db.User) (*time.Location, error) {
	loc, err := time.LoadLocation(user.Timezone)
	if err != nil {
		return nil, fmt.Errorf("transactions: timezone %q: %w", user.Timezone, err)
	}
	return loc, nil
}

func key(merchant *string) string {
	if merchant == nil {
		return ""
	}
	return MerchantKey(*merchant)
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func checkKind(fields map[string]string, kind Kind) {
	if kind != KindExpense && kind != KindIncome {
		fields["kind"] = "must be expense or income"
	}
}

func checkAmount(fields map[string]string, amount int64) {
	switch {
	case amount <= 0:
		fields["amount_minor"] = "must be > 0"
	case amount > maxAmountMinor:
		fields["amount_minor"] = "is out of range"
	}
}

// checkText returns text as it is stored, trimmed and nil when empty, noting
// in fields when it is too long.
func checkText(fields map[string]string, field string, text *string, maxLength int) *string {
	if text == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*text)
	if trimmed == "" {
		return nil
	}
	if utf8.RuneCountInString(trimmed) > maxLength {
		fields[field] = fmt.Sprintf("must be at most %d characters", maxLength)
	}
	return &trimmed
}

// checkCategoryFitsKind notes an expense without a category and income with
// one. It says nothing while the kind itself is wrong.
func checkCategoryFitsKind(fields map[string]string, kind Kind, categoryID *uuid.UUID) {
	switch {
	case kind == KindExpense && categoryID == nil:
		fields["category_id"] = "is required for an expense"
	case kind == KindIncome && categoryID != nil:
		fields["category_id"] = "must be empty for income"
	}
}

// checkCategoryActive notes a category that is not one of the user's active
// ones: unknown, someone else's or archived.
func checkCategoryActive(ctx context.Context, q db.Querier, fields map[string]string, userID, categoryID uuid.UUID) error {
	category, err := q.GetCategory(ctx, db.GetCategoryParams{ID: categoryID, UserID: userID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("transactions: get category: %w", err)
	case category.ArchivedAt == nil:
		return nil
	}
	fields["category_id"] = "is not one of your active categories"
	return nil
}
