-- +goose Up

-- A phone that can receive pushes (PRD §2.12). device_id is the client's
-- own UUID; an APNs token belongs to one device of one user at a time, so a
-- token registered again moves to whoever registered it last.
CREATE TABLE devices (
    id             UUID PRIMARY KEY,
    user_id        UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_id      UUID        NOT NULL,
    apns_token     TEXT        NOT NULL UNIQUE,
    environment    TEXT        NOT NULL CHECK (environment IN ('sandbox', 'production')),
    app_version    TEXT,
    locale         TEXT,
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when APNs says the token is no longer valid; pushes skip it until
    -- the device registers again.
    invalidated_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, device_id)
);

CREATE TRIGGER devices_set_updated_at BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The in-app inbox; every notification is kept here whether or not it was
-- pushed. dedup_key stops the same event notifying twice.
CREATE TABLE notifications (
    id         UUID PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       TEXT        NOT NULL CHECK (kind IN ('budget_alert', 'weekly_recap', 'goal_completed', 'import_ready')),
    title      TEXT        NOT NULL,
    body       TEXT        NOT NULL,
    deep_link  TEXT        NOT NULL,
    dedup_key  TEXT        NOT NULL,
    read_at    TIMESTAMPTZ,
    sent_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, dedup_key)
);

CREATE INDEX notifications_user_id_created_idx ON notifications (user_id, created_at DESC, id DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

-- +goose Down
DROP TABLE notifications;
DROP TABLE devices;
