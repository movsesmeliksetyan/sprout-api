-- +goose Up

-- The canonical kinds of spending. Global knowledge about merchants resolves
-- to one of these, then to the user's own category of that type.
CREATE TABLE category_types (
    key  TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

INSERT INTO category_types (key, name) VALUES
    ('food',      'Food'),
    ('transport', 'Transport'),
    ('housing',   'Housing'),
    ('utilities', 'Utilities'),
    ('leisure',   'Leisure'),
    ('selfcare',  'Self-care'),
    ('health',    'Health'),
    ('shopping',  'Shopping'),
    ('travel',    'Travel'),
    ('education', 'Education'),
    ('pets',      'Pets'),
    ('gifts',     'Gifts'),
    ('family',    'Family'),
    ('work',      'Work');

CREATE TABLE categories (
    id                   UUID PRIMARY KEY,
    user_id              UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name                 TEXT        NOT NULL,
    -- An icon name the client knows (contract §1.3).
    icon                 TEXT        NOT NULL,
    -- Index into the client's slate ramp; 0 is lightest.
    shade                SMALLINT    NOT NULL CHECK (shade BETWEEN 0 AND 6),
    sort_order           INTEGER     NOT NULL,
    monthly_budget_minor BIGINT      NOT NULL DEFAULT 0 CHECK (monthly_budget_minor >= 0),
    -- Null when the category matches no canonical type.
    category_type        TEXT REFERENCES category_types (key),
    -- Set when the category is archived: hidden from lists and pickers, kept
    -- for the transactions that point at it.
    archived_at          TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A name is unique among a user's active categories, whatever its case.
CREATE UNIQUE INDEX categories_user_id_name_key ON categories (user_id, lower(name))
    WHERE archived_at IS NULL;

CREATE INDEX categories_user_id_sort_order_idx ON categories (user_id, sort_order);

CREATE TRIGGER categories_set_updated_at BEFORE UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE categories;
DROP TABLE category_types;
