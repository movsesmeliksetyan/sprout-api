package transactions

import (
	"context"

	"github.com/oapi-codegen/nullable"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the /transactions operations.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for the /transactions operations.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// ListTransactions returns a page of the user's transactions, newest first,
// with the totals of the days on it.
func (h *Handler) ListTransactions(ctx context.Context, request httpx.ListTransactionsRequestObject) (httpx.ListTransactionsResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}

	params := ListParams{
		CategoryID: request.Params.CategoryID,
		Limit:      request.Params.Limit,
	}
	if request.Params.From != nil {
		params.From = &request.Params.From.Time
	}
	if request.Params.To != nil {
		params.To = &request.Params.To.Time
	}
	if request.Params.Kind != nil {
		kind := Kind(*request.Params.Kind)
		params.Kind = &kind
	}
	if request.Params.Q != nil {
		params.Query = *request.Params.Q
	}
	// An empty cursor is the first page, as no cursor is.
	if request.Params.Cursor != nil && *request.Params.Cursor != "" {
		cursor, err := httpx.DecodeCursor(*request.Params.Cursor)
		if err != nil {
			return nil, err
		}
		params.After = &cursor
	}

	page, err := h.service.List(ctx, user.ID, params)
	if err != nil {
		return nil, err
	}

	list := httpx.TransactionList{
		Items: make([]httpx.Transaction, 0, len(page.Items)),
		Days:  make([]httpx.DayTotal, 0, len(page.Days)),
	}
	for _, item := range page.Items {
		list.Items = append(list.Items, toTransaction(item))
	}
	for _, day := range page.Days {
		list.Days = append(list.Days, httpx.DayTotal{
			Date:     openapi_types.Date{Time: day.Date},
			NetMinor: day.NetMinor,
			Count:    day.Count,
		})
	}
	if page.Next != nil {
		next := httpx.EncodeCursor(*page.Next)
		list.NextCursor = &next
	}
	return httpx.ListTransactions200JSONResponse(list), nil
}

// CreateTransaction adds a manual transaction to the user's ledger.
func (h *Handler) CreateTransaction(ctx context.Context, request httpx.CreateTransactionRequestObject) (httpx.CreateTransactionResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	created, err := h.service.Create(ctx, user, CreateParams{
		Kind:        Kind(request.Body.Kind),
		AmountMinor: request.Body.AmountMinor,
		CategoryID:  request.Body.CategoryID,
		Merchant:    request.Body.Merchant,
		Note:        request.Body.Note,
		OccurredAt:  request.Body.OccurredAt,
	})
	if err != nil {
		return nil, err
	}
	return httpx.CreateTransaction201JSONResponse(toTransaction(created)), nil
}

// GetTransaction returns one of the user's transactions.
func (h *Handler) GetTransaction(ctx context.Context, request httpx.GetTransactionRequestObject) (httpx.GetTransactionResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	transaction, err := h.service.Get(ctx, user.ID, request.ID)
	if err != nil {
		return nil, err
	}
	return httpx.GetTransaction200JSONResponse(toTransaction(transaction)), nil
}

// UpdateTransaction changes any subset of a transaction's fields.
func (h *Handler) UpdateTransaction(ctx context.Context, request httpx.UpdateTransactionRequestObject) (httpx.UpdateTransactionResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	patch := Patch{
		AmountMinor: request.Body.AmountMinor,
		CategoryID:  toOptional(request.Body.CategoryID),
		Merchant:    toOptional(request.Body.Merchant),
		Note:        toOptional(request.Body.Note),
		OccurredAt:  request.Body.OccurredAt,
	}
	if request.Body.Kind != nil {
		kind := Kind(*request.Body.Kind)
		patch.Kind = &kind
	}
	updated, err := h.service.Update(ctx, user, request.ID, patch)
	if err != nil {
		return nil, err
	}
	return httpx.UpdateTransaction200JSONResponse(toTransaction(updated)), nil
}

// DeleteTransaction removes one of the user's transactions.
func (h *Handler) DeleteTransaction(ctx context.Context, request httpx.DeleteTransactionRequestObject) (httpx.DeleteTransactionResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if err := h.service.Delete(ctx, user.ID, request.ID); err != nil {
		return nil, err
	}
	return httpx.DeleteTransaction204Response{}, nil
}

// toOptional tells a field that was left out from one sent as null.
func toOptional[T any](field nullable.Nullable[T]) Optional[T] {
	if !field.IsSpecified() {
		return Optional[T]{}
	}
	if field.IsNull() {
		return Optional[T]{Set: true}
	}
	value := field.MustGet()
	return Optional[T]{Set: true, Value: &value}
}

func toTransaction(transaction db.Transaction) httpx.Transaction {
	return httpx.Transaction{
		ID:          transaction.ID,
		Kind:        httpx.TransactionKind(transaction.Kind),
		AmountMinor: transaction.AmountMinor,
		CategoryID:  transaction.CategoryID,
		Merchant:    transaction.Merchant,
		Note:        transaction.Note,
		OccurredAt:  httpx.Instant(transaction.OccurredAt),
		LocalDate:   openapi_types.Date{Time: transaction.LocalDate},
		Source:      httpx.TransactionSource(transaction.Source),
		ReceiptID:   transaction.ReceiptID,
		ImportID:    transaction.ImportID,
		CreatedAt:   httpx.Instant(transaction.CreatedAt),
	}
}
