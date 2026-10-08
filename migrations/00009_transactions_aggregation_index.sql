-- +goose Up

-- The balance and the period summaries add up amounts by kind and day. With
-- the amount and the category in the index they are answered from it alone,
-- without visiting the rows: the balance reads every transaction a user has.
CREATE INDEX transactions_user_id_kind_local_date_idx ON transactions (user_id, kind, local_date)
    INCLUDE (amount_minor, category_id);

-- +goose Down
DROP INDEX transactions_user_id_kind_local_date_idx;
