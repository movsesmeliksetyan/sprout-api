-- name: CreateReceipt :one
INSERT INTO receipts (id, user_id, upload_id)
VALUES (@id, @user_id, @upload_id)
RETURNING *;

-- name: GetReceipt :one
-- One of the user's receipts, with its image's key and when the image was
-- deleted from storage, if it was.
SELECT r.*, u.object_key AS image_key, u.purged_at AS image_purged_at
FROM receipts r
JOIN uploads u ON u.id = r.upload_id
WHERE r.id = @id AND r.user_id = @user_id;

-- name: GetReceiptForUpdate :one
SELECT * FROM receipts WHERE id = @id AND user_id = @user_id FOR UPDATE;

-- name: DeleteReceipt :exec
DELETE FROM receipts WHERE id = @id;

-- name: GetReceiptSource :one
-- What processing a receipt needs: the stored image and the user's
-- currency and timezone.
SELECT r.id, r.user_id, r.status, u.object_key, u.content_type, usr.currency, usr.timezone
FROM receipts r
JOIN uploads u ON u.id = r.upload_id
JOIN users usr ON usr.id = r.user_id
WHERE r.id = @id;

-- name: FailReceipt :exec
-- Ends processing with a failure, unless the receipt was confirmed or
-- deleted meanwhile.
UPDATE receipts SET status = 'failed', failure = @failure WHERE id = @id AND status = 'processing';

-- name: FinishReceipt :exec
-- Records what extraction read, unless the receipt was confirmed or
-- deleted meanwhile.
UPDATE receipts
SET status              = 'ready',
    merchant            = @merchant,
    purchased_at        = @purchased_at,
    total_minor         = @total_minor,
    currency            = @currency,
    payment_method      = @payment_method,
    category_id         = @category_id,
    category_confidence = @category_confidence,
    line_items          = @line_items,
    raw_extraction      = @raw_extraction,
    failure             = NULL
WHERE id = @id AND status = 'processing';
