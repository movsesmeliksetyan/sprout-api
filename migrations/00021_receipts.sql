-- +goose Up

-- A photographed receipt on its way into the ledger (PRD §6): extracted in
-- the background, reviewed, then confirmed into one transaction. Nothing
-- reaches transactions before confirm.
CREATE TABLE receipts (
    id                  UUID PRIMARY KEY,
    user_id             UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    upload_id           UUID        NOT NULL REFERENCES uploads (id) ON DELETE CASCADE,
    status              TEXT        NOT NULL DEFAULT 'processing'
                        CHECK (status IN ('processing', 'ready', 'failed', 'confirmed')),
    -- What extraction found; any of it may be missing.
    merchant            TEXT,
    purchased_at        TIMESTAMPTZ,
    total_minor         BIGINT CHECK (total_minor > 0),
    currency            TEXT,
    -- Card brand and last four digits only.
    payment_method      TEXT,
    category_id         UUID REFERENCES categories (id) ON DELETE SET NULL,
    category_confidence DOUBLE PRECISION,
    line_items          JSONB       NOT NULL DEFAULT '[]',
    -- The model's answer as given, and anything noted about it.
    raw_extraction      JSONB,
    -- The transaction confirm created.
    transaction_id      UUID REFERENCES transactions (id) ON DELETE SET NULL,
    -- {code, message} when status is failed.
    failure             JSONB,
    confirmed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX receipts_user_id_created_at_idx ON receipts (user_id, created_at DESC);

CREATE TRIGGER receipts_set_updated_at BEFORE UPDATE ON receipts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE transactions ADD CONSTRAINT transactions_receipt_id_fkey
    FOREIGN KEY (receipt_id) REFERENCES receipts (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE transactions DROP CONSTRAINT transactions_receipt_id_fkey;
DROP TABLE receipts;
