package goals

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the /goals operations.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for the /goals operations.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// ListGoals returns the user's active and completed goals with the totals
// across them.
func (h *Handler) ListGoals(ctx context.Context, _ httpx.ListGoalsRequestObject) (httpx.ListGoalsResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	list, err := h.service.List(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	items := make([]httpx.Goal, 0, len(list.Items))
	for _, goal := range list.Items {
		item, err := h.toAPI(ctx, goal)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return httpx.ListGoals200JSONResponse{
		TotalSavedMinor:  list.TotalSavedMinor,
		TotalTargetMinor: list.TotalTargetMinor,
		Pct:              list.Pct,
		Items:            items,
	}, nil
}

// CreateGoal adds a goal for the user.
func (h *Handler) CreateGoal(ctx context.Context, request httpx.CreateGoalRequestObject) (httpx.CreateGoalResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	created, err := h.service.Create(ctx, user.ID, CreateParams{
		Title:         request.Body.Title,
		Emoji:         request.Body.Emoji,
		ImageUploadID: request.Body.ImageUploadID,
		TargetMinor:   request.Body.TargetMinor,
	})
	if err != nil {
		return nil, err
	}
	goal, err := h.toAPI(ctx, created)
	if err != nil {
		return nil, err
	}
	return httpx.CreateGoal201JSONResponse(goal), nil
}

// GetGoal returns one of the user's goals with its newest contributions.
func (h *Handler) GetGoal(ctx context.Context, request httpx.GetGoalRequestObject) (httpx.GetGoalResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	detail, err := h.service.Get(ctx, user.ID, request.ID)
	if err != nil {
		return nil, err
	}
	goal, err := h.toAPI(ctx, detail.Goal)
	if err != nil {
		return nil, err
	}
	recent := make([]httpx.Contribution, 0, len(detail.RecentContributions))
	for _, contribution := range detail.RecentContributions {
		recent = append(recent, ContributionToAPI(contribution))
	}
	return httpx.GetGoal200JSONResponse{
		ID:                  goal.ID,
		Title:               goal.Title,
		Emoji:               goal.Emoji,
		ImageURL:            goal.ImageURL,
		TargetMinor:         goal.TargetMinor,
		SavedMinor:          goal.SavedMinor,
		RemainingMinor:      goal.RemainingMinor,
		Pct:                 goal.Pct,
		MonthlyPaceMinor:    goal.MonthlyPaceMinor,
		EtaMonth:            goal.EtaMonth,
		Status:              goal.Status,
		SortOrder:           goal.SortOrder,
		CreatedAt:           goal.CreatedAt,
		RecentContributions: recent,
	}, nil
}

// UpdateGoal changes one of the user's goals.
func (h *Handler) UpdateGoal(ctx context.Context, request httpx.UpdateGoalRequestObject) (httpx.UpdateGoalResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	body := request.Body
	patch := Patch{
		Title:       body.Title,
		TargetMinor: body.TargetMinor,
		SortOrder:   body.SortOrder,
	}
	switch emoji := body.Emoji; {
	case emoji.IsNull():
		patch.ClearEmoji = true
	case emoji.IsSpecified():
		value := emoji.MustGet()
		patch.Emoji = &value
	}
	switch image := body.ImageUploadID; {
	case image.IsNull():
		patch.ClearImage = true
	case image.IsSpecified():
		id := image.MustGet()
		patch.ImageUploadID = &id
	}
	if body.Status != nil {
		status := Status(*body.Status)
		patch.Status = &status
	}

	updated, err := h.service.Update(ctx, user.ID, request.ID, patch)
	if err != nil {
		return nil, err
	}
	goal, err := h.toAPI(ctx, updated)
	if err != nil {
		return nil, err
	}
	return httpx.UpdateGoal200JSONResponse(goal), nil
}

// DeleteGoal removes one of the user's goals and its contributions.
func (h *Handler) DeleteGoal(ctx context.Context, request httpx.DeleteGoalRequestObject) (httpx.DeleteGoalResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if err := h.service.Delete(ctx, user.ID, request.ID); err != nil {
		return nil, err
	}
	return httpx.DeleteGoal204Response{}, nil
}

func (h *Handler) toAPI(ctx context.Context, goal Goal) (httpx.Goal, error) {
	imageURL, err := h.service.ImageURL(ctx, goal.Goal)
	if err != nil {
		return httpx.Goal{}, err
	}
	return httpx.Goal{
		ID:               goal.ID,
		Title:            goal.Title,
		Emoji:            goal.Emoji,
		ImageURL:         imageURL,
		TargetMinor:      goal.TargetMinor,
		SavedMinor:       goal.SavedMinor,
		RemainingMinor:   goal.RemainingMinor,
		Pct:              goal.Pct,
		MonthlyPaceMinor: goal.MonthlyPaceMinor,
		EtaMonth:         goal.EtaMonth,
		Status:           httpx.GoalStatus(goal.Status),
		SortOrder:        int(goal.SortOrder),
		CreatedAt:        goal.CreatedAt,
	}, nil
}

// ContributionToAPI converts a stored contribution to its wire form.
func ContributionToAPI(contribution db.GoalContribution) httpx.Contribution {
	return httpx.Contribution{
		ID:          contribution.ID,
		Kind:        httpx.ContributionKind(contribution.Kind),
		AmountMinor: contribution.AmountMinor,
		OccurredAt:  contribution.OccurredAt,
		LocalDate:   openapi_types.Date{Time: contribution.LocalDate},
	}
}
