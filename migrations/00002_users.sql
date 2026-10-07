-- +goose Up

CREATE TABLE users (
    id                     UUID PRIMARY KEY,
    -- The Auth0 user id from the token's sub claim.
    auth0_sub              TEXT        NOT NULL UNIQUE,
    -- Empty when the identity provider did not share them.
    email                  TEXT        NOT NULL DEFAULT '',
    name                   TEXT        NOT NULL DEFAULT '',
    avatar_key             TEXT,
    currency               TEXT        NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- IANA name. Decides which calendar day a new row's local_date falls on.
    timezone               TEXT        NOT NULL DEFAULT 'UTC',
    starting_balance_minor BIGINT      NOT NULL DEFAULT 0,
    onboarding_completed   BOOLEAN     NOT NULL DEFAULT false,
    notifications_enabled  BOOLEAN     NOT NULL DEFAULT true,
    budget_alerts          BOOLEAN     NOT NULL DEFAULT true,
    weekly_recap           BOOLEAN     NOT NULL DEFAULT true,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE users;
