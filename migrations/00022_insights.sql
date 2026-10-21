-- +goose Up

-- ledger_version moves whenever something an insight is drawn from changes:
-- transactions, categories, goals and their contributions, the user's
-- currency or timezone. Stored insights carry the version they were
-- computed at, so a read can tell when they are stale (PRD BE-54).
ALTER TABLE users ADD COLUMN ledger_version BIGINT NOT NULL DEFAULT 0;

-- +goose StatementBegin
-- Bumps the version of every user whose rows the statement touched, once
-- per statement, so a 1,000-row import commit costs one update.
CREATE FUNCTION bump_ledger_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE users SET ledger_version = ledger_version + 1
    WHERE id IN (SELECT DISTINCT user_id FROM changed);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['transactions', 'categories', 'goals', 'goal_contributions'] LOOP
        EXECUTE format('CREATE TRIGGER %1$s_bump_ledger_insert AFTER INSERT ON %1$s
            REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION bump_ledger_version()', t);
        EXECUTE format('CREATE TRIGGER %1$s_bump_ledger_update AFTER UPDATE ON %1$s
            REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION bump_ledger_version()', t);
        EXECUTE format('CREATE TRIGGER %1$s_bump_ledger_delete AFTER DELETE ON %1$s
            REFERENCING OLD TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION bump_ledger_version()', t);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION bump_own_ledger_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.ledger_version := OLD.ledger_version + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_bump_ledger BEFORE UPDATE OF currency, timezone ON users
    FOR EACH ROW WHEN (OLD.currency IS DISTINCT FROM NEW.currency OR OLD.timezone IS DISTINCT FROM NEW.timezone)
    EXECUTE FUNCTION bump_own_ledger_version();

-- One computation of a period's insights: the ledger version and the day
-- (in the user's timezone) it was made on. A read whose version and day
-- match serves the stored cards.
CREATE TABLE insight_runs (
    user_id        UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    period         TEXT        NOT NULL CHECK (period IN ('week', 'month', 'year')),
    period_start   DATE        NOT NULL,
    ledger_version BIGINT      NOT NULL,
    computed_on    DATE        NOT NULL,
    computed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, period, period_start)
);

-- The cards of one computation, in the order they are shown.
CREATE TABLE insights (
    id           UUID PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    period       TEXT        NOT NULL CHECK (period IN ('week', 'month', 'year')),
    period_start DATE        NOT NULL,
    position     INTEGER     NOT NULL,
    kind         TEXT        NOT NULL CHECK (kind IN ('category_spend_down', 'category_spend_up',
                     'category_over_budget', 'category_near_budget', 'peak_weekday', 'total_spend_down',
                     'total_spend_up', 'goal_on_track', 'goal_almost_there', 'no_data')),
    tone         TEXT        NOT NULL CHECK (tone IN ('positive', 'warning', 'neutral')),
    icon         TEXT        NOT NULL CHECK (icon IN ('sparkle', 'warning', 'chart', 'target')),
    title        TEXT        NOT NULL,
    body         TEXT        NOT NULL,
    category_id  UUID REFERENCES categories (id) ON DELETE CASCADE,
    goal_id      UUID REFERENCES goals (id) ON DELETE CASCADE,
    score        INTEGER     NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (user_id, period, period_start, kind, category_id, goal_id)
);

CREATE INDEX insights_user_period_idx ON insights (user_id, period, period_start, position);

-- +goose Down
DROP TABLE insights;
DROP TABLE insight_runs;
DROP TRIGGER users_bump_ledger ON users;
DROP FUNCTION bump_own_ledger_version();
-- +goose StatementBegin
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['transactions', 'categories', 'goals', 'goal_contributions'] LOOP
        EXECUTE format('DROP TRIGGER %1$s_bump_ledger_insert ON %1$s', t);
        EXECUTE format('DROP TRIGGER %1$s_bump_ledger_update ON %1$s', t);
        EXECUTE format('DROP TRIGGER %1$s_bump_ledger_delete ON %1$s', t);
    END LOOP;
END;
$$;
-- +goose StatementEnd
DROP FUNCTION bump_ledger_version();
ALTER TABLE users DROP COLUMN ledger_version;
