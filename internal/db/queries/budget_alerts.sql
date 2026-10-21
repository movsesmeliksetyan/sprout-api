-- name: BudgetAlertThresholds :many
-- The highest threshold each of the user's categories was alerted about in
-- the month.
SELECT category_id, max(threshold)::smallint AS threshold
FROM budget_alerts
WHERE user_id = $1 AND month = $2
GROUP BY category_id;

-- name: RecordBudgetAlert :exec
INSERT INTO budget_alerts (user_id, category_id, month, threshold)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING;
