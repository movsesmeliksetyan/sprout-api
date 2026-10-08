-- name: GetUserByAuth0Sub :one
SELECT * FROM users WHERE auth0_sub = $1;

-- name: CreateUser :one
-- Returns no row when a user with this auth0_sub already exists.
INSERT INTO users (id, auth0_sub, email, name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (auth0_sub) DO NOTHING
RETURNING *;

-- name: GetUserForUpdate :one
-- Locks the row until the transaction ends.
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: UpdateUser :one
-- A null argument leaves its column as it is.
UPDATE users
SET name                   = COALESCE(sqlc.narg(name), name),
    currency               = COALESCE(sqlc.narg(currency), currency),
    timezone               = COALESCE(sqlc.narg(timezone), timezone),
    starting_balance_minor = COALESCE(sqlc.narg(starting_balance_minor), starting_balance_minor),
    onboarding_completed   = COALESCE(sqlc.narg(onboarding_completed), onboarding_completed),
    notifications_enabled  = COALESCE(sqlc.narg(notifications_enabled), notifications_enabled),
    budget_alerts          = COALESCE(sqlc.narg(budget_alerts), budget_alerts),
    weekly_recap           = COALESCE(sqlc.narg(weekly_recap), weekly_recap),
    avatar_key             = CASE WHEN sqlc.arg(clear_avatar)::boolean THEN NULL ELSE avatar_key END
WHERE id = sqlc.arg(id)
RETURNING *;
