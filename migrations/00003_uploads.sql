-- +goose Up

-- A file the client sends straight to object storage through a presigned
-- URL, then hands to the endpoint that uses it.
CREATE TABLE uploads (
    id           UUID PRIMARY KEY,
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose      TEXT        NOT NULL CHECK (purpose IN ('avatar', 'receipt', 'statement')),
    object_key   TEXT        NOT NULL UNIQUE,
    content_type TEXT        NOT NULL,
    -- What the client declared; replaced by the stored object's size on consume.
    size_bytes   BIGINT      NOT NULL CHECK (size_bytes > 0),
    filename     TEXT,
    status       TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'uploaded', 'consumed')),
    -- When the presigned upload URL stops working.
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX uploads_user_id_idx ON uploads (user_id);

CREATE TRIGGER uploads_set_updated_at BEFORE UPDATE ON uploads
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE uploads;
