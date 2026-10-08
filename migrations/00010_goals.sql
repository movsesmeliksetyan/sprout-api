-- +goose Up

-- A goal's picture is uploaded like an avatar, under a purpose of its own.
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose_check;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose_check
    CHECK (purpose IN ('avatar', 'goal_image', 'receipt', 'statement'));

-- Something the user is saving toward. What is saved is not stored: it is
-- the sum of the goal's contributions.
CREATE TABLE goals (
    id           UUID PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title        TEXT        NOT NULL,
    emoji        TEXT,
    -- The object key of the goal's picture.
    image_key    TEXT,
    target_minor BIGINT      NOT NULL CHECK (target_minor > 0),
    -- A goal that is not archived is completed exactly when what is saved
    -- has reached the target.
    status       TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'archived')),
    sort_order   INTEGER     NOT NULL,
    -- When the goal last became completed; null while it is not.
    completed_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX goals_user_id_sort_order_idx ON goals (user_id, sort_order);

CREATE TRIGGER goals_set_updated_at BEFORE UPDATE ON goals
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Money moved into a goal (a top-up) or back out of it (a withdrawal).
CREATE TABLE goal_contributions (
    id           UUID PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Deleting a goal removes its contributions, which returns the money to
    -- the balance.
    goal_id      UUID        NOT NULL REFERENCES goals (id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL CHECK (kind IN ('topup', 'withdrawal')),
    -- Always positive; direction comes from kind.
    amount_minor BIGINT      NOT NULL CHECK (amount_minor > 0),
    occurred_at  TIMESTAMPTZ NOT NULL,
    -- The calendar day of occurred_at in the user's timezone when the row was
    -- written.
    local_date   DATE        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX goal_contributions_goal_id_occurred_at_id_idx ON goal_contributions (goal_id, occurred_at DESC, id DESC);
CREATE INDEX goal_contributions_user_id_created_at_idx ON goal_contributions (user_id, created_at);

-- +goose Down
DROP TABLE goal_contributions;
DROP TABLE goals;
DELETE FROM uploads WHERE purpose = 'goal_image';
ALTER TABLE uploads DROP CONSTRAINT uploads_purpose_check;
ALTER TABLE uploads ADD CONSTRAINT uploads_purpose_check
    CHECK (purpose IN ('avatar', 'receipt', 'statement'));
