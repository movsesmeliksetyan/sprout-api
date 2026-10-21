-- name: CreateNotification :one
-- Stores a notification unless the user already has one with the same
-- dedup_key; then no row comes back.
INSERT INTO notifications (id, user_id, kind, title, body, deep_link, dedup_key)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, dedup_key) DO NOTHING
RETURNING *;

-- name: GetNotification :one
SELECT * FROM notifications WHERE id = $1 AND user_id = $2;

-- name: MarkNotificationSent :exec
UPDATE notifications SET sent_at = now() WHERE id = $1 AND user_id = $2;

-- name: ListPushDevices :many
-- The user's devices APNs has not rejected.
SELECT * FROM devices
WHERE user_id = $1 AND invalidated_at IS NULL
ORDER BY created_at, id;

-- name: InvalidateDevice :exec
-- Stops pushes to a token APNs rejected, unless the device has registered
-- another since.
UPDATE devices SET invalidated_at = now()
WHERE id = $1 AND user_id = $2 AND apns_token = $3 AND invalidated_at IS NULL;

-- name: ListNotifications :many
-- A page of the inbox, newest first. The cursor is the last notification
-- of the previous page.
SELECT n.* FROM notifications n
WHERE n.user_id = @user_id
  AND (sqlc.narg(cursor_id)::uuid IS NULL
       OR (n.created_at, n.id) < (SELECT c.created_at, c.id FROM notifications c
                                  WHERE c.id = sqlc.narg(cursor_id)::uuid AND c.user_id = @user_id))
ORDER BY n.created_at DESC, n.id DESC
LIMIT @row_limit;

-- name: CountNotifications :one
-- How many of ids are the user's.
SELECT count(*) FROM notifications WHERE user_id = $1 AND id = ANY(@ids::uuid[]);

-- name: MarkNotificationsRead :exec
UPDATE notifications SET read_at = now()
WHERE user_id = $1 AND id = ANY(@ids::uuid[]) AND read_at IS NULL;

-- name: MarkAllNotificationsRead :exec
UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL;

-- name: HasUnreadNotifications :one
SELECT EXISTS (SELECT 1 FROM notifications WHERE user_id = $1 AND read_at IS NULL);
