-- +goose Up

-- What commit writes to the transaction's note: "Refund" on a credit from
-- a merchant the user spent with (PRD §5.4).
ALTER TABLE import_rows ADD COLUMN note TEXT;

-- +goose Down

ALTER TABLE import_rows DROP COLUMN note;
