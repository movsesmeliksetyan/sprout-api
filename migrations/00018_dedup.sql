-- +goose Up

-- The bank's own id for a transaction committed from a statement (an OFX
-- FITID, a reference column): matched first when the same row is imported
-- again.
ALTER TABLE transactions ADD COLUMN external_ref TEXT;

CREATE INDEX transactions_user_id_external_ref_idx ON transactions (user_id, external_ref)
    WHERE external_ref IS NOT NULL;

-- The same SHA-256 as transactions.dedup_hash, in the same type.
ALTER TABLE import_rows ALTER COLUMN dedup_hash TYPE BYTEA USING decode(dedup_hash, 'hex');

-- +goose Down

ALTER TABLE import_rows ALTER COLUMN dedup_hash TYPE TEXT USING encode(dedup_hash, 'hex');
DROP INDEX transactions_user_id_external_ref_idx;
ALTER TABLE transactions DROP COLUMN external_ref;
