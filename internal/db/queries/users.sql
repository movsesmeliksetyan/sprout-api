-- name: GetUserByAuth0Sub :one
SELECT * FROM users WHERE auth0_sub = $1;

-- name: CreateUser :one
-- Returns no row when a user with this auth0_sub already exists.
INSERT INTO users (id, auth0_sub, email, name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (auth0_sub) DO NOTHING
RETURNING *;
