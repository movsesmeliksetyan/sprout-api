-- name: ReleaseAPNsToken :exec
-- Frees a token held by any other device, of this user or another, so it
-- can move to the one registering it.
DELETE FROM devices
WHERE apns_token = sqlc.arg(apns_token)
  AND NOT (user_id = sqlc.arg(user_id) AND device_id = sqlc.arg(device_id));

-- name: UpsertDevice :one
INSERT INTO devices (id, user_id, device_id, apns_token, environment, app_version, locale)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, device_id) DO UPDATE
SET apns_token     = EXCLUDED.apns_token,
    environment    = EXCLUDED.environment,
    app_version    = EXCLUDED.app_version,
    locale         = EXCLUDED.locale,
    last_seen_at   = now(),
    invalidated_at = NULL
RETURNING *;

-- name: DeleteDevice :execrows
DELETE FROM devices WHERE user_id = $1 AND device_id = $2;

-- name: ListDevices :many
SELECT * FROM devices WHERE user_id = $1 ORDER BY created_at, id;
