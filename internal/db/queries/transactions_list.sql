-- name: ListTransactions :many
-- A page of the user's transactions, newest first. A null filter is not
-- applied. The cursor is the last row of the previous page.
SELECT * FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(from_date)::date IS NULL OR local_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR local_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(category_id)::uuid IS NULL OR category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(pattern)::text IS NULL OR merchant ILIKE sqlc.narg(pattern)::text OR note ILIKE sqlc.narg(pattern)::text)
  AND (sqlc.narg(cursor_date)::date IS NULL
       OR (local_date, id) < (sqlc.narg(cursor_date)::date, sqlc.narg(cursor_id)::uuid))
ORDER BY local_date DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListDayTotals :many
-- The whole-day totals of the given days under the same filters as
-- ListTransactions: income minus expense, and how many transactions.
SELECT local_date,
       sum(CASE kind WHEN 'income' THEN amount_minor ELSE -amount_minor END)::bigint AS net_minor,
       count(*) AS count
FROM transactions
WHERE user_id = sqlc.arg(user_id)
  AND local_date = ANY (sqlc.arg(dates)::date[])
  AND (sqlc.narg(from_date)::date IS NULL OR local_date >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR local_date <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(category_id)::uuid IS NULL OR category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(pattern)::text IS NULL OR merchant ILIKE sqlc.narg(pattern)::text OR note ILIKE sqlc.narg(pattern)::text)
GROUP BY local_date
ORDER BY local_date DESC;
