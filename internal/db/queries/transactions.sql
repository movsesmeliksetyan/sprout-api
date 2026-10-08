-- name: CreateTransaction :one
INSERT INTO transactions (id, user_id, kind, amount_minor, category_id, merchant, merchant_key, note,
                          occurred_at, local_date, source, import_id, receipt_id, dedup_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1 AND user_id = $2;

-- name: GetTransactionForUpdate :one
-- Locks the row until the database transaction ends.
SELECT * FROM transactions WHERE id = $1 AND user_id = $2 FOR UPDATE;

-- name: UpdateTransaction :one
-- Writes every field a user can change; the caller sends the ones that stay
-- as they are.
UPDATE transactions
SET kind         = sqlc.arg(kind),
    amount_minor = sqlc.arg(amount_minor),
    category_id  = sqlc.narg(category_id),
    merchant     = sqlc.narg(merchant),
    merchant_key = sqlc.arg(merchant_key),
    note         = sqlc.narg(note),
    occurred_at  = sqlc.arg(occurred_at),
    local_date   = sqlc.arg(local_date),
    dedup_hash   = sqlc.arg(dedup_hash)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
RETURNING *;

-- name: DeleteTransaction :execrows
DELETE FROM transactions WHERE id = $1 AND user_id = $2;

-- name: UserHasTransactions :one
SELECT EXISTS (SELECT 1 FROM transactions WHERE user_id = $1);

-- name: CategoryHasTransactions :one
SELECT EXISTS (SELECT 1 FROM transactions WHERE user_id = $1 AND category_id = $2);
