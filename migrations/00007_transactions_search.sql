-- +goose Up

-- Searching the ledger is "merchant or note contains this text"; trigram
-- indexes answer that without reading every row.
CREATE INDEX transactions_merchant_trgm_idx ON transactions USING gin (merchant gin_trgm_ops);
CREATE INDEX transactions_note_trgm_idx ON transactions USING gin (note gin_trgm_ops);

-- +goose Down
DROP INDEX transactions_note_trgm_idx;
DROP INDEX transactions_merchant_trgm_idx;
