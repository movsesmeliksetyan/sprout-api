// Package storage wraps S3-compatible object storage behind a Store interface.
package storage

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Head when no object has the key.
var ErrNotFound = errors.New("storage: object not found")

// Object describes a stored object.
type Object struct {
	Size        int64
	ContentType string
}

// Store is the object storage the API needs. Clients move the bytes
// themselves through presigned URLs; the API only signs, inspects and
// deletes.
type Store interface {
	// PresignPut returns a URL that accepts one PUT of exactly size bytes
	// with the given Content-Type, until ttl has passed.
	PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, error)
	// PresignGet returns a URL that serves the object until ttl has passed.
	// It does not check that the object exists.
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	// Head describes the object, or returns ErrNotFound.
	Head(ctx context.Context, key string) (Object, error)
	// Delete removes the object. Deleting one that does not exist succeeds.
	Delete(ctx context.Context, key string) error
	// Ready reports whether the bucket can be reached.
	Ready(ctx context.Context) error
}
