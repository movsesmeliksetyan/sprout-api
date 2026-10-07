package users

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
)

// Middleware turns the verified claims of a request into its user, creating
// the user on their first request. It runs after auth.Middleware.
func Middleware(service *Service, responder *httpx.Responder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.ClaimsFromContext(r.Context())
			if !ok {
				responder.Error(w, r, httpx.ErrUnauthenticated)
				return
			}
			user, err := service.Provision(r.Context(), claims)
			if err != nil {
				responder.Error(w, r, err)
				return
			}

			ctx := WithUser(r.Context(), user)
			ctx = logging.With(ctx, slog.String("user_id", user.ID.String()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Handler implements the /me operations.
type Handler struct{}

// NewHandler returns the handler for the /me operations.
func NewHandler() *Handler { return &Handler{} }

// GetMe returns the signed-in user.
func (*Handler) GetMe(ctx context.Context, _ httpx.GetMeRequestObject) (httpx.GetMeResponseObject, error) {
	user, ok := FromContext(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	return httpx.GetMe200JSONResponse(toMe(user)), nil
}

func toMe(user db.User) httpx.Me {
	return httpx.Me{
		ID:        user.ID,
		Name:      user.Name,
		FirstName: FirstName(user.Name),
		Email:     user.Email,
		// The avatar arrives with uploads (BE-12).
		AvatarURL:            nil,
		Currency:             user.Currency,
		Timezone:             user.Timezone,
		StartingBalanceMinor: user.StartingBalanceMinor,
		OnboardingCompleted:  user.OnboardingCompleted,
		Preferences: httpx.Preferences{
			NotificationsEnabled: user.NotificationsEnabled,
			BudgetAlerts:         user.BudgetAlerts,
			WeeklyRecap:          user.WeeklyRecap,
		},
		// Counted once categories, goals and the streak exist (BE-20).
		Stats:     httpx.ProfileStats{},
		CreatedAt: httpx.Instant(user.CreatedAt),
	}
}
