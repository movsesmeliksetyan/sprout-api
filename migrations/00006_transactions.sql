-- +goose Up

-- The ledger: one row per expense or income.
CREATE TABLE transactions (
    id           UUID PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL CHECK (kind IN ('expense', 'income')),
    -- Always positive; direction comes from kind.
    amount_minor BIGINT      NOT NULL CHECK (amount_minor > 0),
    -- A category that has transactions cannot be deleted, only archived.
    category_id  UUID REFERENCES categories (id) ON DELETE RESTRICT,
    merchant     TEXT,
    -- The merchant in the form used for matching; empty when there is none.
    merchant_key TEXT        NOT NULL DEFAULT '',
    note         TEXT,
    occurred_at  TIMESTAMPTZ NOT NULL,
    -- The calendar day of occurred_at in the user's timezone when the row was
    -- written. All grouping uses it.
    local_date   DATE        NOT NULL,
    source       TEXT        NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'import', 'receipt')),
    -- Where the row came from. The imports and receipts tables do not exist
    -- yet; their migrations add the foreign keys.
    import_id    UUID,
    receipt_id   UUID,
    -- SHA-256 of local_date, amount_minor, kind and merchant_key: what a
    -- statement row is compared with to find duplicates.
    dedup_hash   BYTEA       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- An expense has a category; income has none.
    CONSTRAINT transactions_category_matches_kind CHECK ((kind = 'income') = (category_id IS NULL))
);

CREATE INDEX transactions_user_id_local_date_id_idx ON transactions (user_id, local_date DESC, id DESC);
CREATE INDEX transactions_user_id_category_id_local_date_idx ON transactions (user_id, category_id, local_date);
CREATE INDEX transactions_user_id_dedup_hash_idx ON transactions (user_id, dedup_hash);

CREATE TRIGGER transactions_set_updated_at BEFORE UPDATE ON transactions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE transactions;
