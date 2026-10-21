package categorize

import (
	"context"
	"strings"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the categorisation operations of the API.
type Handler struct {
	engine *Engine
}

// NewHandler returns the handler for engine.
func NewHandler(engine *Engine) *Handler {
	return &Handler{engine: engine}
}

// SuggestCategory suggests a category for what the user is typing into a
// manual expense. It runs the deterministic layers only, never the model.
// The note feeds the keyword layer, and names the merchant when no
// merchant is given.
func (h *Handler) SuggestCategory(ctx context.Context, request httpx.SuggestCategoryRequestObject) (httpx.SuggestCategoryResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	input := Input{Direction: DirectionDebit}
	if request.Body.Merchant != nil {
		input.Merchant = strings.TrimSpace(*request.Body.Merchant)
	}
	if request.Body.Note != nil {
		input.RawDescription = strings.TrimSpace(*request.Body.Note)
	}
	if request.Body.AmountMinor != nil {
		input.AmountMinor = *request.Body.AmountMinor
	}
	results, err := h.engine.Classify(ctx, user.ID, []Input{input}, Options{})
	if err != nil {
		return nil, err
	}
	result := results[0]
	return httpx.SuggestCategory200JSONResponse{
		CategoryID: result.CategoryID,
		Confidence: result.Confidence,
		Source:     httpx.CategorizationSource(result.Source),
	}, nil
}
