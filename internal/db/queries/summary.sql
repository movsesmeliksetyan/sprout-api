-- name: CategorySpending :many
-- Every active category of the user with its expenses on the calendar days
-- from_date to to_date, and on the days from previous_from_date up to
-- from_date, the period before. An archived category is included only when
-- it has expenses in the period. Largest first; equal amounts come in
-- display order.
SELECT c.id AS category_id,
       c.monthly_budget_minor,
       COALESCE(sum(t.amount_minor) FILTER (WHERE t.local_date >= sqlc.arg(from_date)::date), 0)::bigint AS spent_minor,
       (count(t.id) FILTER (WHERE t.local_date >= sqlc.arg(from_date)::date))::integer AS txn_count,
       COALESCE(sum(t.amount_minor) FILTER (WHERE t.local_date < sqlc.arg(from_date)::date), 0)::bigint AS previous_spent_minor
FROM categories c
LEFT JOIN transactions t ON t.category_id = c.id
    AND t.user_id = c.user_id
    AND t.kind = 'expense'
    AND t.local_date BETWEEN sqlc.arg(previous_from_date)::date AND sqlc.arg(to_date)::date
WHERE c.user_id = sqlc.arg(user_id)
GROUP BY c.id
HAVING c.archived_at IS NULL
    OR count(t.id) FILTER (WHERE t.local_date >= sqlc.arg(from_date)::date) > 0
ORDER BY spent_minor DESC, c.sort_order, c.id;

-- name: CategorySpentByDay :many
-- The user's expenses in one category on the calendar days from_date to
-- to_date, both included, day by day; days without any are left out.
SELECT local_date,
       sum(amount_minor)::bigint AS spent_minor,
       count(*)::integer AS txn_count
FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND category_id = sqlc.arg(category_id)::uuid
  AND kind = 'expense'
  AND local_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
GROUP BY local_date
ORDER BY local_date;

-- name: SpentByDay :many
-- The user's expenses on the calendar days from_date to to_date, both
-- included, day by day; days without any are left out.
SELECT local_date,
       sum(amount_minor)::bigint AS spent_minor
FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND kind = 'expense'
  AND local_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
GROUP BY local_date
ORDER BY local_date;
