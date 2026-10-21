-- +goose Up

-- What each user filed a merchant under, as a category type, from their
-- committed imports (PRD §5.3). One vote a user and merchant, the latest.
-- Types, not categories, so custom category names never leave the user.
CREATE TABLE merchant_votes (
    id            UUID PRIMARY KEY,
    user_id       UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    merchant_key  TEXT        NOT NULL,
    category_type TEXT        NOT NULL REFERENCES category_types (key),
    display_name  TEXT        NOT NULL,
    voted_at      TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, merchant_key)
);

CREATE INDEX merchant_votes_merchant_key_idx ON merchant_votes (merchant_key, category_type);

CREATE TRIGGER merchant_votes_set_updated_at BEFORE UPDATE ON merchant_votes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE merchant_votes;
