-- +goose Up

-- When a statement file is deleted from storage (PRD §4.5): 7 days after
-- the import is created if nothing else happens, 24 hours after it is
-- committed or discarded. purged_at is set once it is gone.
ALTER TABLE uploads
    ADD COLUMN purge_after TIMESTAMPTZ,
    ADD COLUMN purged_at   TIMESTAMPTZ;

CREATE INDEX uploads_purge_after_idx ON uploads (purge_after)
    WHERE purge_after IS NOT NULL AND purged_at IS NULL;

UPDATE uploads SET purge_after = updated_at + interval '7 days'
WHERE purpose = 'statement' AND status = 'consumed';

-- +goose Down

DROP INDEX uploads_purge_after_idx;
ALTER TABLE uploads
    DROP COLUMN purged_at,
    DROP COLUMN purge_after;
