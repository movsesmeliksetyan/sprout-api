package receipts

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// Handler implements the receipt operations of the API.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for service.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// CreateReceipt starts scanning a receipt upload.
func (h *Handler) CreateReceipt(ctx context.Context, request httpx.CreateReceiptRequestObject) (httpx.CreateReceiptResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	created, err := h.service.Create(ctx, user.ID, request.Body.UploadID)
	if err != nil {
		return nil, err
	}
	out, err := ToAPI(created)
	if err != nil {
		return nil, err
	}
	return httpx.CreateReceipt202JSONResponse(out), nil
}

// GetReceipt returns a receipt, to poll while it is read.
func (h *Handler) GetReceipt(ctx context.Context, request httpx.GetReceiptRequestObject) (httpx.GetReceiptResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	found, err := h.service.Get(ctx, user.ID, request.ID)
	if err != nil {
		return nil, err
	}
	out, err := ToAPI(found)
	if err != nil {
		return nil, err
	}
	return httpx.GetReceipt200JSONResponse(out), nil
}

// ConfirmReceipt writes a receipt to the ledger as an expense.
func (h *Handler) ConfirmReceipt(ctx context.Context, request httpx.ConfirmReceiptRequestObject) (httpx.ConfirmReceiptResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	created, err := h.service.Confirm(ctx, user, request.ID, Confirmation{
		AmountMinor: request.Body.AmountMinor, CategoryID: request.Body.CategoryID, OccurredAt: request.Body.OccurredAt,
		Merchant: request.Body.Merchant, Note: request.Body.Note,
	})
	if err != nil {
		return nil, err
	}
	return httpx.ConfirmReceipt201JSONResponse(transactions.ToAPI(created)), nil
}

// DeleteReceipt discards a receipt that has not been confirmed.
func (h *Handler) DeleteReceipt(ctx context.Context, request httpx.DeleteReceiptRequestObject) (httpx.DeleteReceiptResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if err := h.service.Delete(ctx, user.ID, request.ID); err != nil {
		return nil, err
	}
	return httpx.DeleteReceipt204Response{}, nil
}

// ToAPI is a receipt on the wire.
func ToAPI(r Receipt) (httpx.Receipt, error) {
	out := httpx.Receipt{
		ID: r.ID, Status: httpx.ReceiptStatus(r.Status), ImageURL: r.ImageURL, Merchant: r.Merchant,
		TotalMinor: r.TotalMinor, Currency: r.Currency, PaymentMethod: r.PaymentMethod,
		CategoryID: r.CategoryID, CategoryConfidence: r.CategoryConfidence, TransactionID: r.TransactionID,
		LineItems: []httpx.ReceiptLineItem{},
	}
	if r.PurchasedAt != nil {
		purchased := httpx.Instant(*r.PurchasedAt)
		out.PurchasedAt = &purchased
	}
	var items []LineItem
	if err := json.Unmarshal(r.LineItems, &items); err != nil {
		return httpx.Receipt{}, fmt.Errorf("receipts: line items: %w", err)
	}
	for _, item := range items {
		out.LineItems = append(out.LineItems, httpx.ReceiptLineItem{Name: item.Name, Quantity: item.Quantity, AmountMinor: item.AmountMinor})
	}
	if len(r.Failure) > 0 {
		var failure Failure
		if err := json.Unmarshal(r.Failure, &failure); err != nil {
			return httpx.Receipt{}, fmt.Errorf("receipts: failure: %w", err)
		}
		out.Failure = &httpx.ReceiptFailure{Code: httpx.ReceiptFailureCode(failure.Code), Message: failure.Message}
	}
	return out, nil
}
