-- +goose Up

-- The budget thresholds a user has been alerted about, per category and
-- month (PRD BE-57). An import's combined alert covers several categories
-- with one notification, so the inbox's dedup_key alone cannot tell which
-- thresholds were told; this can.
CREATE TABLE budget_alerts (
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    category_id UUID        NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    month       DATE        NOT NULL,
    threshold   SMALLINT    NOT NULL CHECK (threshold IN (80, 100)),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, category_id, month, threshold)
);

-- +goose Down
DROP TABLE budget_alerts;
