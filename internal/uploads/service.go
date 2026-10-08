package uploads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
	"github.com/movsesmeliksetyan/sprout-api/internal/storage"
)

const (
	// uploadWindow is how long a presigned upload URL works.
	uploadWindow      = 15 * time.Minute
	maxFilenameLength = 255

	statusPending = "pending"
)

// Reasons Consume refuses an upload. The endpoint that asked for it reports
// them against its own field.
var (
	// ErrNotFound: no such upload for this user. An upload that belongs to
	// someone else is reported the same way.
	ErrNotFound = errors.New("uploads: not found")
	// ErrWrongPurpose: the upload was requested for a different purpose.
	ErrWrongPurpose = errors.New("uploads: wrong purpose")
	// ErrNotUploaded: no file was sent to the upload URL.
	ErrNotUploaded = errors.New("uploads: file not uploaded")
	// ErrTooLarge: the stored file exceeds the limit for its purpose.
	ErrTooLarge = errors.New("uploads: file too large")
	// ErrAlreadyConsumed: the upload has been used before.
	ErrAlreadyConsumed = errors.New("uploads: already used")
)

// Service holds the business logic for uploads.
type Service struct {
	db    db.DBTX
	store storage.Store
	now   func() time.Time
}

// NewService returns a Service that records uploads in database and keeps
// their files in store.
func NewService(database db.DBTX, store storage.Store) *Service {
	return &Service{db: database, store: store, now: time.Now}
}

// CreateParams is what a client declares about the file it is going to send.
type CreateParams struct {
	Purpose     Purpose
	ContentType string
	SizeBytes   int64
	Filename    *string
}

// Created is a new upload and where to send its file.
type Created struct {
	Upload db.Upload
	// URL accepts one PUT with ContentType as its Content-Type and exactly
	// SizeBytes of body, until Upload.ExpiresAt.
	URL string
}

// Create registers an upload for the user and presigns its URL. The declared
// type and size must fit the purpose: an invalid declaration is an
// *httpx.ValidationError, a file over the limit is httpx.ErrPayloadTooLarge.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, params CreateParams) (Created, error) {
	fields := map[string]string{}
	contentType := strings.ToLower(strings.TrimSpace(params.ContentType))

	rules, known := policies[params.Purpose]
	switch {
	case !known:
		fields["purpose"] = "must be one of avatar, goal_image, receipt, statement"
	case !rules.allows(contentType):
		fields["content_type"] = "is not accepted for this purpose"
	}
	if params.SizeBytes <= 0 {
		fields["size_bytes"] = "must be greater than zero"
	}
	var filename *string
	if params.Filename != nil {
		if name := strings.TrimSpace(*params.Filename); name != "" {
			if utf8.RuneCountInString(name) > maxFilenameLength {
				fields["filename"] = fmt.Sprintf("must be at most %d characters", maxFilenameLength)
			}
			filename = &name
		}
	}
	if len(fields) > 0 {
		return Created{}, &httpx.ValidationError{Fields: fields}
	}
	if params.SizeBytes > rules.maxBytes {
		return Created{}, httpx.WithMessage(httpx.ErrPayloadTooLarge,
			fmt.Sprintf("The file is too large. The limit is %d MB.", rules.maxBytes/megabyte))
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Created{}, fmt.Errorf("uploads: new id: %w", err)
	}
	key := fmt.Sprintf("u/%s/%s/%s", userID, params.Purpose, id)

	url, err := s.store.PresignPut(ctx, key, contentType, params.SizeBytes, uploadWindow)
	if err != nil {
		return Created{}, fmt.Errorf("uploads: %w", err)
	}
	upload, err := db.New(s.db).CreateUpload(ctx, db.CreateUploadParams{
		ID:          id,
		UserID:      userID,
		Purpose:     string(params.Purpose),
		ObjectKey:   key,
		ContentType: contentType,
		SizeBytes:   params.SizeBytes,
		Filename:    filename,
		ExpiresAt:   s.now().Add(uploadWindow),
	})
	if err != nil {
		return Created{}, fmt.Errorf("uploads: create: %w", err)
	}
	return Created{Upload: upload, URL: url}, nil
}

// Consume claims the user's upload for a feature: it checks that the upload
// was requested for purpose, that the file arrived and is within the limit,
// and marks it used so it cannot be claimed again. q is the caller's
// transaction, so the claim is undone if the caller's own write fails.
//
// A refusal is one of the Err values above.
func (s *Service) Consume(ctx context.Context, q db.Querier, userID, id uuid.UUID, purpose Purpose) (db.Upload, error) {
	upload, err := q.GetUpload(ctx, db.GetUploadParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Upload{}, ErrNotFound
	}
	if err != nil {
		return db.Upload{}, fmt.Errorf("uploads: get: %w", err)
	}
	if upload.Purpose != string(purpose) {
		return db.Upload{}, ErrWrongPurpose
	}
	if upload.Status != statusPending {
		return db.Upload{}, ErrAlreadyConsumed
	}

	object, err := s.store.Head(ctx, upload.ObjectKey)
	if errors.Is(err, storage.ErrNotFound) {
		return db.Upload{}, ErrNotUploaded
	}
	if err != nil {
		return db.Upload{}, fmt.Errorf("uploads: %w", err)
	}
	// The upload URL is bound to the declared size, so this holds unless the
	// object got there some other way. Trust the stored size, not the row.
	if object.Size > policies[purpose].maxBytes {
		if err := s.store.Delete(ctx, upload.ObjectKey); err != nil {
			return db.Upload{}, fmt.Errorf("uploads: %w", err)
		}
		return db.Upload{}, ErrTooLarge
	}

	consumed, err := q.ConsumeUpload(ctx, db.ConsumeUploadParams{ID: id, UserID: userID, SizeBytes: object.Size})
	if errors.Is(err, pgx.ErrNoRows) {
		// Claimed by a concurrent request since it was read.
		return db.Upload{}, ErrAlreadyConsumed
	}
	if err != nil {
		return db.Upload{}, fmt.Errorf("uploads: consume: %w", err)
	}
	return consumed, nil
}

// Rejection returns the text to show a user for a Consume refusal, and
// false for any other error.
func Rejection(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrNotFound):
		return "upload not found", true
	case errors.Is(err, ErrWrongPurpose):
		return "upload was made for a different purpose", true
	case errors.Is(err, ErrNotUploaded):
		return "the file has not been uploaded", true
	case errors.Is(err, ErrTooLarge):
		return "the file is too large", true
	case errors.Is(err, ErrAlreadyConsumed):
		return "upload has already been used", true
	}
	return "", false
}
