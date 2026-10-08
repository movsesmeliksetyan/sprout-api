-- name: AcquireIdempotencyKey :one
-- Claims the key for a request. Returns a row only to the request that now
-- owns it: the key is new, has expired, or belongs to a request that never
-- finished.
INSERT INTO idempotency_keys (user_id, key, request_hash, locked_until, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, key) DO UPDATE
SET request_hash          = EXCLUDED.request_hash,
    status                = 'processing',
    locked_until          = EXCLUDED.locked_until,
    response_status       = NULL,
    response_content_type = NULL,
    response_body         = NULL,
    expires_at            = EXCLUDED.expires_at,
    created_at            = now()
WHERE idempotency_keys.expires_at <= now()
   OR (idempotency_keys.status = 'processing' AND idempotency_keys.locked_until <= now())
RETURNING *;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE user_id = $1 AND key = $2;

-- name: CompleteIdempotencyKey :execrows
-- Stores the response to replay. locked_until tells the owner apart from a
-- request that took the key over.
UPDATE idempotency_keys
SET status = 'completed', response_status = $4, response_content_type = $5, response_body = $6
WHERE user_id = $1 AND key = $2 AND status = 'processing' AND locked_until = $3;

-- name: ReleaseIdempotencyKey :execrows
-- Gives the key up so that a retry runs again.
DELETE FROM idempotency_keys
WHERE user_id = $1 AND key = $2 AND status = 'processing' AND locked_until = $3;

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at <= now();
