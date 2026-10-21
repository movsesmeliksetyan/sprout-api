package notify

import (
	"context"

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
