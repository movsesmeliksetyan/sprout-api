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
