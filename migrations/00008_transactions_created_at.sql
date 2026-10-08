-- +goose Up

-- The streak on the profile counts the days a user added transactions on,
-- which is read from here without visiting the rows.
CREATE INDEX transactions_user_id_created_at_idx ON transactions (user_id, created_at);

-- +goose Down
DROP INDEX transactions_user_id_created_at_idx;
