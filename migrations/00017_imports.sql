-- +goose Up

-- A statement on its way into the ledger (PRD §4): processed in the
-- background, reviewed, then committed. Nothing reaches transactions
-- before commit.
CREATE TABLE imports (
    id                 UUID PRIMARY KEY,
    user_id            UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    upload_id          UUID        NOT NULL REFERENCES uploads (id) ON DELETE CASCADE,
    status             TEXT        NOT NULL DEFAULT 'processing'
                       CHECK (status IN ('processing', 'needs_mapping', 'ready', 'failed', 'committed')),
    -- The window of transactions to keep.
    period             TEXT        NOT NULL CHECK (period IN ('week', 'month', 'year', 'all')),
    filename           TEXT,
    -- What detection found: csv, ofx, xlsx or pdf; a bank's name; how the
    -- mapping was found.
    format             TEXT,
    bank_hint          TEXT,
    mapping_source     TEXT CHECK (mapping_source IN ('template', 'heuristic', 'llm', 'user', 'native')),
    mapping_spec       JSONB,
    mapping_request    JSONB,
    header_fingerprint TEXT,
    summary            JSONB       NOT NULL DEFAULT '{}',
    -- {code, message} when status is failed.
    failure            JSONB,
    committed_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX imports_user_id_created_at_idx ON imports (user_id, created_at DESC);

CREATE TRIGGER imports_set_updated_at BEFORE UPDATE ON imports
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row of an import's statement, normalised and categorised, for
-- review.
CREATE TABLE import_rows (
    id                  UUID PRIMARY KEY,
    import_id           UUID        NOT NULL REFERENCES imports (id) ON DELETE CASCADE,
    user_id             UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    row_index           INTEGER     NOT NULL,
    -- The row's cells as read; cleared by retention.
    raw                 JSONB,
    status              TEXT        NOT NULL CHECK (status IN ('ok', 'duplicate', 'error')),
    included            BOOLEAN     NOT NULL,
    kind                TEXT CHECK (kind IN ('expense', 'income', 'transfer')),
    -- Always positive; direction comes from kind.
    amount_minor        BIGINT CHECK (amount_minor > 0),
    occurred_at         TIMESTAMPTZ,
    local_date          DATE,
    merchant            TEXT,
    merchant_key        TEXT        NOT NULL DEFAULT '',
    raw_description     TEXT        NOT NULL DEFAULT '',
    mcc                 TEXT,
    external_ref        TEXT,
    category_id         UUID REFERENCES categories (id) ON DELETE SET NULL,
    category_confidence DOUBLE PRECISION,
    category_source     TEXT CHECK (category_source IN ('user_rule', 'dictionary', 'mcc', 'keyword', 'llm', 'none')),
    dedup_hash          TEXT,
    duplicate_of        UUID REFERENCES transactions (id) ON DELETE SET NULL,
    error               TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (import_id, row_index),
    CHECK (status <> 'error' OR NOT included)
);

CREATE INDEX import_rows_import_id_idx ON import_rows (import_id, local_date DESC, id DESC);

CREATE TRIGGER import_rows_set_updated_at BEFORE UPDATE ON import_rows
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- How to read one statement layout, found once and reused for every file
-- with the same headers. Global, no user data inside.
CREATE TABLE mapping_templates (
    id                 UUID PRIMARY KEY,
    header_fingerprint TEXT        NOT NULL UNIQUE,
    format             TEXT        NOT NULL,
    bank_hint          TEXT,
    spec               JSONB       NOT NULL,
    source             TEXT        NOT NULL CHECK (source IN ('seed', 'llm', 'user', 'heuristic')),
    use_count          INTEGER     NOT NULL DEFAULT 0,
    success_count      INTEGER     NOT NULL DEFAULT 0,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER mapping_templates_set_updated_at BEFORE UPDATE ON mapping_templates
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE transactions ADD CONSTRAINT transactions_import_id_fkey
    FOREIGN KEY (import_id) REFERENCES imports (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE transactions DROP CONSTRAINT transactions_import_id_fkey;
DROP TABLE mapping_templates;
DROP TABLE import_rows;
DROP TABLE imports;
