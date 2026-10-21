-- name: CreateImport :one
INSERT INTO imports (id, user_id, upload_id, period, filename)
VALUES (@id, @user_id, @upload_id, @period, @filename)
RETURNING *;

-- name: GetImport :one
SELECT * FROM imports WHERE id = @id AND user_id = @user_id;

-- name: GetImportByID :one
-- An import by id alone, for the job that processes it.
SELECT * FROM imports WHERE id = @id;

-- name: DeleteImport :execrows
-- Discards an import that has not been committed.
DELETE FROM imports WHERE id = @id AND user_id = @user_id AND status <> 'committed';

-- name: DeleteImportRows :exec
DELETE FROM import_rows WHERE import_id = @import_id;

-- name: StartImportProcessing :exec
-- Puts an import back to the start of processing: what an earlier run
-- found is cleared.
UPDATE imports
SET status = 'processing', format = NULL, bank_hint = NULL, mapping_source = NULL,
    mapping_request = NULL, summary = '{}', failure = NULL
WHERE id = @id AND status <> 'committed';

-- name: FailImport :exec
UPDATE imports SET status = 'failed', failure = @failure WHERE id = @id AND status <> 'committed';

-- name: GetImportSource :one
-- What processing an import needs: the stored file and the user's
-- timezone and currency.
SELECT i.id, i.user_id, i.status, i.period, u.object_key, usr.timezone, usr.currency, usr.name
FROM imports i
JOIN uploads u ON u.id = i.upload_id
JOIN users usr ON usr.id = i.user_id
WHERE i.id = @id;

-- name: InsertImportRows :copyfrom
INSERT INTO import_rows (id, import_id, user_id, row_index, raw, status, included, kind, amount_minor, occurred_at,
                         local_date, merchant, merchant_key, raw_description, mcc, external_ref, category_id,
                         category_confidence, category_source, dedup_hash, duplicate_of, error)
VALUES (@id, @import_id, @user_id, @row_index, @raw, @status, @included, @kind, @amount_minor, @occurred_at,
        @local_date, @merchant, @merchant_key, @raw_description, @mcc, @external_ref, @category_id,
        @category_confidence, @category_source, @dedup_hash, @duplicate_of, @error);

-- name: FinishImport :exec
-- Records what processing found, unless the import was committed or
-- discarded meanwhile.
UPDATE imports
SET status             = @status,
    format             = @format,
    bank_hint          = @bank_hint,
    mapping_source     = @mapping_source,
    mapping_spec       = @mapping_spec,
    mapping_request    = @mapping_request,
    header_fingerprint = @header_fingerprint,
    summary            = @summary,
    failure            = NULL
WHERE id = @id AND status <> 'committed';

-- name: GetImportForUpdate :one
SELECT * FROM imports WHERE id = @id AND user_id = @user_id FOR UPDATE;
