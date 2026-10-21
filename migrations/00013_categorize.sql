-- +goose Up

-- A user's own merchant → category rule, learned from their corrections.
-- It wins over every global layer.
CREATE TABLE merchant_rules (
    id           UUID PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    merchant_key TEXT        NOT NULL,
    category_id  UUID        NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    -- How often the rule was saved or used.
    hits         INTEGER     NOT NULL DEFAULT 0 CHECK (hits >= 0),
    last_used_at TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, merchant_key)
);

CREATE TRIGGER merchant_rules_set_updated_at BEFORE UPDATE ON merchant_rules
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- What is known about a merchant for every user: from the seed file, from
-- users' agreeing votes, or from the LLM. Global, no user data inside.
CREATE TABLE merchant_dictionary (
    id            UUID PRIMARY KEY,
    merchant_key  TEXT        NOT NULL UNIQUE,
    -- Null only on an llm row where the model did not know the merchant.
    category_type TEXT REFERENCES category_types (key),
    display_name  TEXT        NOT NULL,
    source        TEXT        NOT NULL CHECK (source IN ('seed', 'learned', 'llm')),
    -- How many distinct users agree, for a learned row.
    votes         INTEGER     NOT NULL DEFAULT 0 CHECK (votes >= 0),
    -- What the model said about itself, and which model it was: llm rows
    -- only.
    confidence    DOUBLE PRECISION CHECK (confidence BETWEEN 0 AND 1),
    model         TEXT,
    -- When an llm row stops counting and the model may be asked again.
    expires_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((source = 'llm') = (confidence IS NOT NULL AND model IS NOT NULL)),
    CHECK (source = 'llm' OR expires_at IS NULL),
    CHECK (category_type IS NOT NULL OR expires_at IS NOT NULL)
);

CREATE INDEX merchant_dictionary_expires_at_idx ON merchant_dictionary (expires_at)
    WHERE expires_at IS NOT NULL;

CREATE TRIGGER merchant_dictionary_set_updated_at BEFORE UPDATE ON merchant_dictionary
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Merchant category codes → category type. Static, from the seed file.
CREATE TABLE mcc_map (
    mcc           TEXT PRIMARY KEY CHECK (mcc ~ '^[0-9]{4}$'),
    category_type TEXT NOT NULL REFERENCES category_types (key)
);

-- Words in a description that give away its category type, tried highest
-- priority first. Static, from the seed file.
CREATE TABLE keyword_rules (
    pattern       TEXT PRIMARY KEY,
    category_type TEXT    NOT NULL REFERENCES category_types (key),
    priority      INTEGER NOT NULL
);

-- +goose Down
DROP TABLE keyword_rules;
DROP TABLE mcc_map;
DROP TABLE merchant_dictionary;
DROP TABLE merchant_rules;
