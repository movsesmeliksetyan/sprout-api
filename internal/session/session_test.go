package session_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/session"
)

func TestUser(t *testing.T) {
	_, ok := session.User(context.Background())
	assert.False(t, ok, "outside a request there is no user")

	want := db.User{ID: uuid.New(), Name: "Daisy Walker"}
	got, ok := session.User(session.WithUser(context.Background(), want))
	require.True(t, ok)
	assert.Equal(t, want, got)
}
