-- name: CreateUpload :one
INSERT INTO uploads (id, user_id, purpose, object_key, content_type, size_bytes, filename, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetUpload :one
SELECT * FROM uploads WHERE id = $1 AND user_id = $2;

-- name: ConsumeUpload :one
-- Returns no row unless the upload is the user's and still pending.
UPDATE uploads
SET status = 'consumed', size_bytes = $3
WHERE id = $1 AND user_id = $2 AND status = 'pending'
RETURNING *;
