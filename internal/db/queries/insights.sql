-- name: GetLedgerVersion :one
SELECT ledger_version FROM users WHERE id = $1;

-- name: GetInsightRun :one
SELECT * FROM insight_runs
WHERE user_id = $1 AND period = $2 AND period_start = $3;

-- name: ListInsights :many
SELECT * FROM insights
WHERE user_id = $1 AND period = $2 AND period_start = $3
ORDER BY position;

-- name: DeleteInsights :exec
DELETE FROM insights
WHERE user_id = $1 AND period = $2 AND period_start = $3;

-- name: CreateInsight :exec
INSERT INTO insights (id, user_id, period, period_start, position, kind, tone, icon, title, body, category_id, goal_id, score)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: SaveInsightRun :exec
INSERT INTO insight_runs (user_id, period, period_start, ledger_version, computed_on)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, period, period_start) DO UPDATE
SET ledger_version = EXCLUDED.ledger_version,
    computed_on    = EXCLUDED.computed_on,
    computed_at    = now();

-- name: ExpensesByDay :many
-- The user's expenses on the calendar days from_date to to_date, both
-- included, day by day: what was spent and how many; days without any are
-- left out.
SELECT local_date,
       sum(amount_minor)::bigint AS spent_minor,
       count(*)::integer AS txn_count
FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND kind = 'expense'
  AND local_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
GROUP BY local_date
ORDER BY local_date;

-- name: LargestExpenses :many
-- Each category's largest expense on the calendar days from_date to
-- to_date; the earliest one on a tie.
SELECT DISTINCT ON (category_id) category_id::uuid AS category_id, amount_minor, merchant, local_date
FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND kind = 'expense'
  AND category_id IS NOT NULL
  AND local_date BETWEEN sqlc.arg(from_date)::date AND sqlc.arg(to_date)::date
ORDER BY category_id, amount_minor DESC, local_date, id;

-- name: ActiveInsightUsers :many
-- The users who added a transaction since the given time: the ones whose
-- insights are worth computing ahead of their next read.
SELECT DISTINCT u.*
FROM users u
JOIN transactions t ON t.user_id = u.id
WHERE t.created_at >= sqlc.arg(since)
ORDER BY u.id;
