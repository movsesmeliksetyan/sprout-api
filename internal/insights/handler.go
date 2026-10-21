package insights

import (
	"context"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the insights operations of the API.
type Handler struct {
	service *Service
}

// NewHandler returns a handler over the service.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// ListInsights returns the signed-in user's insight cards for a period.
func (h *Handler) ListInsights(ctx context.Context, request httpx.ListInsightsRequestObject) (httpx.ListInsightsResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	cards, err := h.service.List(ctx, user, string(request.Params.Period), request.Params.Offset)
	if err != nil {
		return nil, err
	}
	items := make([]httpx.Insight, 0, len(cards))
	for _, c := range cards {
		items = append(items, ToAPI(c))
	}
	return httpx.ListInsights200JSONResponse{Items: items}, nil
}

// ToAPI is a stored card as the contract sends it.
func ToAPI(c db.Insight) httpx.Insight {
	return httpx.Insight{
		ID: c.ID, Kind: httpx.InsightKind(c.Kind), Tone: httpx.InsightTone(c.Tone), Icon: httpx.InsightIcon(c.Icon),
		Title: c.Title, Body: c.Body, CategoryID: c.CategoryID, GoalID: c.GoalID, CreatedAt: c.CreatedAt,
	}
}
