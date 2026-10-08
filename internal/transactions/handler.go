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
		OccurredAt:  transaction.OccurredAt.UTC(),
		LocalDate:   openapi_types.Date{Time: transaction.LocalDate},
		Source:      httpx.TransactionSource(transaction.Source),
		ReceiptID:   transaction.ReceiptID,
		ImportID:    transaction.ImportID,
		CreatedAt:   transaction.CreatedAt.UTC(),
	}
}
