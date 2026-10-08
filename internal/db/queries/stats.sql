-- name: ActivityStreak :one
-- How many calendar days in a row, ending today or yesterday, the user
-- created at least one transaction or goal contribution on. Days are read in
-- the given timezone.
-- Each run of consecutive days is an island: within one, the day minus its
-- rank is the same date.
WITH days AS (
    SELECT (t.created_at AT TIME ZONE sqlc.arg(timezone)::text)::date AS day
    FROM transactions t
    WHERE t.user_id = sqlc.arg(user_id)
    UNION
    SELECT (c.created_at AT TIME ZONE sqlc.arg(timezone)::text)::date
    FROM goal_contributions c
    WHERE c.user_id = sqlc.arg(user_id)
),
islands AS (
    SELECT day, day - (row_number() OVER (ORDER BY day))::integer AS island
    FROM days
    WHERE day <= sqlc.arg(today)::date
)
SELECT count(*)
FROM islands
WHERE island = (
    SELECT island FROM islands
    WHERE day >= sqlc.arg(today)::date - 1
    ORDER BY day DESC
    LIMIT 1
);
