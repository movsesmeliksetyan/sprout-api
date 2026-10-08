package uploads

import (
	"context"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

// Handler implements the /uploads operations.
type Handler struct {
	service *Service
}

// NewHandler returns the handler for the /uploads operations.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// CreateUpload registers an upload and answers with its presigned URL.
func (h *Handler) CreateUpload(ctx context.Context, request httpx.CreateUploadRequestObject) (httpx.CreateUploadResponseObject, error) {
	user, ok := session.User(ctx)
	if !ok {
		return nil, httpx.ErrUnauthenticated
	}
	if request.Body == nil {
		return nil, httpx.ErrBadRequest
	}

	created, err := h.service.Create(ctx, user.ID, CreateParams{
		Purpose:     Purpose(request.Body.Purpose),
		ContentType: request.Body.ContentType,
		SizeBytes:   request.Body.SizeBytes,
		Filename:    request.Body.Filename,
	})
	if err != nil {
		return nil, err
	}
	return httpx.CreateUpload201JSONResponse{
		ID:        created.Upload.ID,
		UploadURL: created.URL,
		Method:    httpx.UploadMethodPUT,
		Headers:   map[string]string{"Content-Type": created.Upload.ContentType},
		ExpiresAt: httpx.Instant(created.Upload.ExpiresAt),
	}, nil
}
