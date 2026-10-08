package users

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/logging"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
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

			ctx := session.WithUser(r.Context(), user)
			ctx = logging.With(ctx, slog.String("user_id", user.ID.String()))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Handler implements the /me operations.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for the /me operations.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// GetMe returns the signed-in user.
func (h *Handler) GetMe(ctx context.Context, _ httpx.GetMeRequestObject) (httpx.GetMeResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	me, err := h.toMe(ctx, user)
	if err != nil {
		return nil, err
	}
	return httpx.GetMe200JSONResponse(me), nil
}

// UpdateMe changes any subset of the signed-in user's profile.
func (h *Handler) UpdateMe(ctx context.Context, request httpx.UpdateMeRequestObject) (httpx.UpdateMeResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	updated, err := h.service.Update(ctx, user.ID, toPatch(*request.Body))
	if err != nil {
		return nil, err
	}
	me, err := h.toMe(ctx, updated)
	if err != nil {
		return nil, err
	}
	return httpx.UpdateMe200JSONResponse(me), nil
}

func toPatch(body httpx.UpdateMeRequest) Patch {
	patch := Patch{
		Name:                 body.Name,
		Currency:             body.Currency,
		Timezone:             body.Timezone,
		StartingBalanceMinor: body.StartingBalanceMinor,
		OnboardingCompleted:  body.OnboardingCompleted,
	}
	if prefs := body.Preferences; prefs != nil {
		patch.NotificationsEnabled = prefs.NotificationsEnabled
		patch.BudgetAlerts = prefs.BudgetAlerts
		patch.WeeklyRecap = prefs.WeeklyRecap
	}
	switch avatar := body.AvatarUploadID; {
	case avatar.IsNull():
		patch.ClearAvatar = true
	case avatar.IsSpecified():
		id := avatar.MustGet()
		patch.AvatarUploadID = &id
	}
	return patch
}

func (h *Handler) toMe(ctx context.Context, user db.User) (httpx.Me, error) {
	avatarURL, err := h.service.AvatarURL(ctx, user)
	if err != nil {
		return httpx.Me{}, err
	}
	return httpx.Me{
		ID:                   user.ID,
		Name:                 user.Name,
		FirstName:            FirstName(user.Name),
		Email:                user.Email,
		AvatarURL:            avatarURL,
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
	}, nil
}
