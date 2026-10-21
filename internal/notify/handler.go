package notify

import (
	"context"

	"github.com/google/uuid"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the devices and notifications operations of the API.
type Handler struct {
	service *Service
}

// NewHandler returns a handler over the service.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterDevice stores or refreshes the signed-in user's push device.
func (h *Handler) RegisterDevice(ctx context.Context, request httpx.RegisterDeviceRequestObject) (httpx.RegisterDeviceResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	body := request.Body
	if _, err := h.service.RegisterDevice(ctx, user.ID, request.DeviceID, Registration{
		APNsToken: body.ApnsToken, Environment: Environment(body.Environment), AppVersion: body.AppVersion, Locale: body.Locale,
	}); err != nil {
		return nil, err
	}
	return httpx.RegisterDevice204Response{}, nil
}

// DeleteDevice removes the signed-in user's push device.
func (h *Handler) DeleteDevice(ctx context.Context, request httpx.DeleteDeviceRequestObject) (httpx.DeleteDeviceResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if err := h.service.DeleteDevice(ctx, user.ID, request.DeviceID); err != nil {
		return nil, err
	}
	return httpx.DeleteDevice204Response{}, nil
}

// ListNotifications returns a page of the signed-in user's inbox, newest
// first.
func (h *Handler) ListNotifications(ctx context.Context, request httpx.ListNotificationsRequestObject) (httpx.ListNotificationsResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	// An empty cursor is the first page, as no cursor is.
	var after *httpx.Cursor
	if request.Params.Cursor != nil && *request.Params.Cursor != "" {
		cursor, err := httpx.DecodeCursor(*request.Params.Cursor)
		if err != nil {
			return nil, err
		}
		after = &cursor
	}
	page, err := h.service.List(ctx, user.ID, request.Params.Limit, after)
	if err != nil {
		return nil, err
	}
	list := httpx.NotificationList{Items: make([]httpx.Notification, 0, len(page.Items))}
	for _, n := range page.Items {
		list.Items = append(list.Items, ToAPI(n))
	}
	if page.Next != nil {
		next := httpx.EncodeCursor(*page.Next)
		list.NextCursor = &next
	}
	return httpx.ListNotifications200JSONResponse(list), nil
}

// MarkNotificationsRead marks some, or all, of the signed-in user's
// notifications read.
func (h *Handler) MarkNotificationsRead(ctx context.Context, request httpx.MarkNotificationsReadRequestObject) (httpx.MarkNotificationsReadResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}
	var ids []uuid.UUID
	if request.Body.Ids != nil {
		ids = *request.Body.Ids
	}
	all := request.Body.All != nil && *request.Body.All
	if err := h.service.MarkRead(ctx, user.ID, ids, all); err != nil {
		return nil, err
	}
	return httpx.MarkNotificationsRead204Response{}, nil
}

// ToAPI is a notification on the wire.
func ToAPI(n db.Notification) httpx.Notification {
	link := n.DeepLink
	return httpx.Notification{
		ID: n.ID, Kind: httpx.NotificationKind(n.Kind), Title: n.Title, Body: n.Body,
		DeepLink: &link, ReadAt: n.ReadAt, CreatedAt: n.CreatedAt,
	}
}
