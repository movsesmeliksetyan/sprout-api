-- name: LedgerNet :one
-- Everything the user's transactions add up to: income minus expense.
SELECT COALESCE(sum(CASE kind WHEN 'income' THEN amount_minor ELSE -amount_minor END), 0)::bigint
FROM transactions
WHERE user_id = $1;

-- name: SpentBetween :one
-- The user's expenses on the calendar days from_date to to_date, both
-- included.
SELECT COALESCE(sum(amount_minor), 0)::bigint
FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND kind = 'expense'
  AND local_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date;

-- name: SpentByCategory :many
-- The user's expenses on the calendar days from_date to to_date per
-- category, largest first; categories without any are left out. Equal
-- amounts come in display order.
SELECT c.id AS category_id, sum(t.amount_minor)::bigint AS spent_minor
FROM transactions t
JOIN categories c ON c.id = t.category_id AND c.user_id = t.user_id
WHERE t.user_id = sqlc.arg(user_id)
  AND t.kind = 'expense'
  AND t.local_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
GROUP BY c.id, c.sort_order
ORDER BY spent_minor DESC, c.sort_order, c.id;
