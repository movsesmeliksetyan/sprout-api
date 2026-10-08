package summary

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/period"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
	"github.com/movsesmeliksetyan/sprout-api/internal/transactions"
)

// Handler implements the /home and /summary operations.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for the /home and /summary operations.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// GetHome returns the signed-in user's balance, this month's spending and
// their newest transactions.
func (h *Handler) GetHome(ctx context.Context, _ httpx.GetHomeRequestObject) (httpx.GetHomeResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	home, err := h.service.Home(ctx, user)
	if err != nil {
		return nil, err
	}

	segments := make([]httpx.HomeSegment, 0, len(home.Month.Segments))
	for _, segment := range home.Month.Segments {
		segments = append(segments, httpx.HomeSegment{
			CategoryID: segment.CategoryID,
			SpentMinor: segment.SpentMinor,
			// The one place a share becomes a fraction: whole percent over
			// 100, so the shares still add up to 1.
			Share: float64(segment.SharePct) / 100,
		})
	}
	return httpx.GetHome200JSONResponse{
		BalanceMinor: home.BalanceMinor,
		Month: httpx.HomeMonth{
			Range:              toRange(home.Month.Range),
			SpentMinor:         home.Month.SpentMinor,
			PreviousSpentMinor: home.Month.PreviousSpentMinor,
			DeltaMinor:         home.Month.DeltaMinor,
			CategoriesCount:    home.Month.CategoriesCount,
			Segments:           segments,
		},
		Recent: httpx.RecentTransactions{
			Items: transactions.ItemsToAPI(home.Recent.Items),
			Days:  transactions.DaysToAPI(home.Recent.Days),
		},
		HasUnreadNotifications: home.HasUnreadNotifications,
	}, nil
}

func toRange(r period.Range) httpx.Range {
	return httpx.Range{
		Period:    httpx.Period(r.Kind),
		Offset:    r.Offset,
		Start:     openapi_types.Date{Time: r.Start},
		End:       openapi_types.Date{Time: r.End},
		IsCurrent: r.IsCurrent,
	}
}
