package testutil

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/auth"
	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/users"
)

// TestUser is a provisioned user and the claims that identify it. Pass
// Claims to AuthedRequest to call the API as this user.
type TestUser struct {
	db.User
	Claims auth.Claims
}

// NewTestUser creates a user with an identity of its own, the way a first
// request would. Each call returns a different user.
func NewTestUser(t testing.TB, database users.Database) TestUser {
	t.Helper()
	id := uuid.NewString()
	claims := auth.Claims{
		Subject: "test|" + id,
		Email:   id + "@example.com",
		Name:    "Test User",
	}
	user, err := users.NewService(database).Provision(context.Background(), claims)
	require.NoError(t, err)
	return TestUser{User: user, Claims: claims}
}
