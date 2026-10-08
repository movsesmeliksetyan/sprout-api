package categories

import (
	"context"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the /categories and /budgets operations.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for the /categories and /budgets
// operations.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// ListCategories returns the signed-in user's categories in display order.
func (h *Handler) ListCategories(ctx context.Context, request httpx.ListCategoriesRequestObject) (httpx.ListCategoriesResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	includeArchived := request.Params.IncludeArchived != nil && *request.Params.IncludeArchived

	categories, err := h.service.List(ctx, user.ID, includeArchived)
	if err != nil {
		return nil, err
	}
	return httpx.ListCategories200JSONResponse(toList(categories)), nil
}

// CreateCategory adds a category to the end of the user's list.
func (h *Handler) CreateCategory(ctx context.Context, request httpx.CreateCategoryRequestObject) (httpx.CreateCategoryResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	created, err := h.service.Create(ctx, user.ID, CreateParams{
		Name:               request.Body.Name,
		Icon:               string(request.Body.Icon),
		Shade:              request.Body.Shade,
		MonthlyBudgetMinor: request.Body.MonthlyBudgetMinor,
	})
	if err != nil {
		return nil, err
	}
	return httpx.CreateCategory201JSONResponse(toCategory(created)), nil
}

// UpdateCategory changes any subset of a category's fields, and archives or
// restores it.
func (h *Handler) UpdateCategory(ctx context.Context, request httpx.UpdateCategoryRequestObject) (httpx.UpdateCategoryResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	patch := Patch{
		Name:               request.Body.Name,
		Shade:              request.Body.Shade,
		MonthlyBudgetMinor: request.Body.MonthlyBudgetMinor,
		Archived:           request.Body.Archived,
	}
	if request.Body.Icon != nil {
		icon := string(*request.Body.Icon)
		patch.Icon = &icon
	}
	updated, err := h.service.Update(ctx, user.ID, request.ID, patch)
	if err != nil {
		return nil, err
	}
	return httpx.UpdateCategory200JSONResponse(toCategory(updated)), nil
}

// DeleteCategory removes a category that has no transactions.
func (h *Handler) DeleteCategory(ctx context.Context, request httpx.DeleteCategoryRequestObject) (httpx.DeleteCategoryResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if err := h.service.Delete(ctx, user.ID, request.ID); err != nil {
		return nil, err
	}
	return httpx.DeleteCategory204Response{}, nil
}

// ReorderCategories puts the user's active categories in a new order.
func (h *Handler) ReorderCategories(ctx context.Context, request httpx.ReorderCategoriesRequestObject) (httpx.ReorderCategoriesResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	reordered, err := h.service.Reorder(ctx, user.ID, request.Body.Ids)
	if err != nil {
		return nil, err
	}
	return httpx.ReorderCategories200JSONResponse(toList(reordered)), nil
}

func toList(categories []db.Category) httpx.CategoryList {
	items := make([]httpx.Category, 0, len(categories))
	for _, category := range categories {
		items = append(items, toCategory(category))
	}
	return httpx.CategoryList{Items: items}
}

func toCategory(category db.Category) httpx.Category {
	return httpx.Category{
		ID:                 category.ID,
		Name:               category.Name,
		Icon:               httpx.CategoryIcon(category.Icon),
		Shade:              int(category.Shade),
		SortOrder:          int(category.SortOrder),
		MonthlyBudgetMinor: category.MonthlyBudgetMinor,
		Archived:           category.ArchivedAt != nil,
	}
}
