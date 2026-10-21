package testutil

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
)

// NewNotification stores an unread weekly_recap notification in the
// user's inbox, as notify.Send would, without enqueuing a push.
func NewNotification(t testing.TB, database db.DBTX, userID uuid.UUID) db.Notification {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	n, err := db.New(database).CreateNotification(context.Background(), db.CreateNotificationParams{
		ID: id, UserID: userID, Kind: "weekly_recap", Title: "Your week", Body: "You spent $120 this week.",
		DeepLink: "sprout://stats", DedupKey: "test:" + id.String(),
	})
	require.NoError(t, err)
	return n
}
