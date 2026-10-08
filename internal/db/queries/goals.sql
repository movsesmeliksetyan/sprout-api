-- name: ListGoals :many
-- The user's active and completed goals in display order, each with what is
-- saved in it: top-ups minus withdrawals.
SELECT sqlc.embed(g),
       COALESCE((SELECT sum(CASE c.kind WHEN 'topup' THEN c.amount_minor ELSE -c.amount_minor END)
                 FROM goal_contributions c
                 WHERE c.goal_id = g.id), 0)::bigint AS saved_minor
FROM goals g
WHERE g.user_id = $1 AND g.status <> 'archived'
ORDER BY g.sort_order, g.id;

-- name: GetGoal :one
SELECT sqlc.embed(g),
       COALESCE((SELECT sum(CASE c.kind WHEN 'topup' THEN c.amount_minor ELSE -c.amount_minor END)
                 FROM goal_contributions c
                 WHERE c.goal_id = g.id), 0)::bigint AS saved_minor
FROM goals g
WHERE g.id = $1 AND g.user_id = $2;

-- name: CountActiveGoals :one
SELECT count(*) FROM goals WHERE user_id = $1 AND status = 'active';

-- name: CountGoals :one
-- How many goals the user's list shows: the active and the completed ones.
SELECT count(*) FROM goals WHERE user_id = $1 AND status <> 'archived';

-- name: NextGoalSortOrder :one
-- The position after the user's last goal.
SELECT (COALESCE(max(sort_order), -1) + 1)::integer FROM goals WHERE user_id = $1;

-- name: CreateGoal :one
INSERT INTO goals (id, user_id, title, emoji, image_key, target_minor, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateGoal :one
-- Writes the goal as given: the caller reads it, changes what it must and
-- sends every column back.
UPDATE goals
SET title        = $3,
    emoji        = $4,
    image_key    = $5,
    target_minor = $6,
    status       = $7,
    sort_order   = $8,
    completed_at = $9
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteGoal :execrows
DELETE FROM goals WHERE id = $1 AND user_id = $2;

-- name: ListRecentContributions :many
-- The newest contributions of one of the user's goals.
SELECT * FROM goal_contributions
WHERE goal_id = $1 AND user_id = $2
ORDER BY local_date DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListContributions :many
-- A page of the contributions of one of the user's goals, newest first. The
-- cursor is the last row of the previous page.
SELECT * FROM goal_contributions
WHERE goal_id = sqlc.arg(goal_id) AND user_id = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_date)::date IS NULL
       OR (local_date, id) < (sqlc.narg(cursor_date)::date, sqlc.narg(cursor_id)::uuid))
ORDER BY local_date DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: CreateContribution :one
INSERT INTO goal_contributions (id, user_id, goal_id, kind, amount_minor, occurred_at, local_date)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: DeleteContribution :one
-- Removes a contribution of one of the user's goals and returns it.
DELETE FROM goal_contributions
WHERE id = $1 AND goal_id = $2 AND user_id = $3
RETURNING *;
