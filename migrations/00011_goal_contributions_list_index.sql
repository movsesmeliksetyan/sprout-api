-- +goose Up

-- A goal's contributions are listed as the ledger is: by calendar day and
-- id, newest first.
DROP INDEX goal_contributions_goal_id_occurred_at_id_idx;
CREATE INDEX goal_contributions_goal_id_local_date_id_idx ON goal_contributions (goal_id, local_date DESC, id DESC);

-- +goose Down
DROP INDEX goal_contributions_goal_id_local_date_id_idx;
CREATE INDEX goal_contributions_goal_id_occurred_at_id_idx ON goal_contributions (goal_id, occurred_at DESC, id DESC);
