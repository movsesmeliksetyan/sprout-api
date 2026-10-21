package notify

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/movsesmeliksetyan/sprout-api/internal/db"
	"github.com/movsesmeliksetyan/sprout-api/internal/money"
)

// The notifications other features raise through their hooks.

// GoalCompleted tells the user a goal is fully saved, inside the goal's
// transaction: goals.CompletionHook. A goal notifies once, however often
// it is reopened and completed again.
func (s *Service) GoalCompleted(ctx context.Context, tx pgx.Tx, goal db.Goal) error {
	user, err := db.New(tx).GetUser(ctx, goal.UserID)
	if err != nil {
		return fmt.Errorf("notify: user: %w", err)
	}
	_, err = s.SendTx(ctx, tx, goal.UserID, Notification{
		Kind:     KindGoalCompleted,
		Title:    "You reached your goal",
		Body:     fmt.Sprintf("%s is fully saved: %s. Well done!", goal.Title, money.Format(goal.TargetMinor, user.Currency)),
		DeepLink: "sprout://goal/" + goal.ID.String(),
		DedupKey: "goal_completed:" + goal.ID.String(),
	})
	return err
}

// ImportReady tells the user a slow import is ready to review:
// imports.ReadyHook.
func (s *Service) ImportReady(ctx context.Context, userID, importID uuid.UUID) error {
	_, err := s.Send(ctx, userID, Notification{
		Kind:     KindImportReady,
		Title:    "Your statement is ready",
		Body:     "Review the transactions we found and add them to your ledger.",
		DeepLink: "sprout://import/" + importID.String(),
		DedupKey: "import_ready:" + importID.String(),
	})
	return err
}
