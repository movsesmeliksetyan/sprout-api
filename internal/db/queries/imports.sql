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
                         category_confidence, category_source, dedup_hash, duplicate_of, error, note)
VALUES (@id, @import_id, @user_id, @row_index, @raw, @status, @included, @kind, @amount_minor, @occurred_at,
        @local_date, @merchant, @merchant_key, @raw_description, @mcc, @external_ref, @category_id,
        @category_confidence, @category_source, @dedup_hash, @duplicate_of, @error, @note);

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

-- name: ListLedgerMatches :many
-- The user's transactions a statement's rows may duplicate: those with one
-- of the rows' hashes or bank references, oldest first.
SELECT id, dedup_hash, external_ref
FROM transactions
WHERE user_id = @user_id
  AND (dedup_hash = ANY (@hashes::bytea[]) OR external_ref = ANY (@external_refs::text[]))
ORDER BY created_at, id;

-- name: ListImportRows :many
-- A page of an import's rows for review: the included expenses still
-- without a category first, then newest first, rows without a date last.
-- The cursor is the last row of the previous page.
SELECT r.*,
       (CASE WHEN r.included AND r.kind = 'expense' AND r.category_id IS NULL THEN 0 ELSE 1 END)::int AS sort_rank,
       coalesce(r.local_date, '0001-01-01'::date)::date AS sort_date
FROM import_rows r
WHERE r.import_id = @import_id
  AND r.user_id = @user_id
  AND (sqlc.narg(cursor_id)::uuid IS NULL
       OR (CASE WHEN r.included AND r.kind = 'expense' AND r.category_id IS NULL THEN 0 ELSE 1 END) > @cursor_rank::int
       OR ((CASE WHEN r.included AND r.kind = 'expense' AND r.category_id IS NULL THEN 0 ELSE 1 END) = @cursor_rank::int
           AND (coalesce(r.local_date, '0001-01-01'::date), r.id) < (sqlc.narg(cursor_date)::date, sqlc.narg(cursor_id)::uuid)))
ORDER BY sort_rank, sort_date DESC, r.id DESC
LIMIT @row_limit;

-- name: GetImportRowForUpdate :one
SELECT * FROM import_rows WHERE id = @id AND import_id = @import_id AND user_id = @user_id FOR UPDATE;

-- name: UpdateImportRow :one
UPDATE import_rows
SET included            = @included,
    kind                = @kind,
    merchant            = @merchant,
    merchant_key        = @merchant_key,
    note                = @note,
    category_id         = @category_id,
    category_confidence = @category_confidence,
    category_source     = @category_source,
    dedup_hash          = @dedup_hash
WHERE id = @id
RETURNING *;

-- name: ImportRowCounts :one
-- An import's summary as its rows stand; totals are of the included rows.
SELECT count(*)::int                                                                  AS row_count,
       count(*) FILTER (WHERE included)::int                                          AS included_count,
       coalesce(sum(amount_minor) FILTER (WHERE included AND kind = 'expense'), 0)::bigint AS total_expense_minor,
       coalesce(sum(amount_minor) FILTER (WHERE included AND kind = 'income'), 0)::bigint  AS total_income_minor,
       count(*) FILTER (WHERE status = 'duplicate')::int                              AS duplicate_count,
       count(*) FILTER (WHERE included AND kind = 'expense' AND category_id IS NULL)::int AS uncategorized_count,
       count(*) FILTER (WHERE status = 'error')::int                                  AS error_count
FROM import_rows
WHERE import_id = @import_id;

-- name: SetImportSummary :exec
UPDATE imports SET summary = @summary WHERE id = @id;
