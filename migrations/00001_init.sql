-- +goose Up

-- Trigram indexes back the merchant and note search on transactions.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Every table with an updated_at column attaches this as a BEFORE UPDATE trigger.
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION set_updated_at();
DROP EXTENSION IF EXISTS pg_trgm;
