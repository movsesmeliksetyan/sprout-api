-- +goose Up

-- One row per Idempotency-Key a user has sent: the request it stood for and,
-- once the request has succeeded, the response to replay.
CREATE TABLE idempotency_keys (
    user_id               UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key                   UUID        NOT NULL,
    -- SHA-256 of the request, so the key cannot be reused for another one.
    request_hash          BYTEA       NOT NULL,
    status                TEXT        NOT NULL DEFAULT 'processing' CHECK (status IN ('processing', 'completed')),
    -- While processing: when the request must have finished. Past it, the
    -- request is taken for dead and the key can be claimed again.
    locked_until          TIMESTAMPTZ NOT NULL,
    response_status       INTEGER,
    response_content_type TEXT,
    response_body         BYTEA,
    expires_at            TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key),
    CHECK (status <> 'completed' OR response_status IS NOT NULL)
);

CREATE INDEX idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);

-- +goose Down
DROP TABLE idempotency_keys;
