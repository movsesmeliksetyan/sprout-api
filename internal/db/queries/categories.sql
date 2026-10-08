-- name: SeedCategory :exec
-- Does nothing when the user already has an active category of this name.
INSERT INTO categories (id, user_id, name, icon, shade, sort_order, category_type)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, lower(name)) WHERE archived_at IS NULL DO NOTHING;

-- name: ListCategories :many
-- Active categories in display order, then the archived ones when asked for.
SELECT * FROM categories
WHERE user_id = $1 AND (sqlc.arg(include_archived)::boolean OR archived_at IS NULL)
ORDER BY (archived_at IS NOT NULL), sort_order, id;

-- name: ListCategoryTypes :many
SELECT * FROM category_types ORDER BY key;

-- name: GetCategory :one
SELECT * FROM categories WHERE id = $1 AND user_id = $2;

-- name: CountActiveCategories :one
SELECT count(*) FROM categories WHERE user_id = $1 AND archived_at IS NULL;

-- name: NextCategorySortOrder :one
-- The position after the user's last active category.
SELECT (COALESCE(max(sort_order), -1) + 1)::integer FROM categories
WHERE user_id = $1 AND archived_at IS NULL;

-- name: CategoryNameTaken :one
-- Whether another active category of the user has this name, in any case.
SELECT EXISTS (
    SELECT 1 FROM categories
    WHERE user_id = $1 AND archived_at IS NULL AND lower(name) = lower(sqlc.arg(name)::text) AND id <> sqlc.arg(except_id)
);

-- name: CreateCategory :one
INSERT INTO categories (id, user_id, name, icon, shade, sort_order, monthly_budget_minor, category_type, archived_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpdateCategory :one
-- A null argument leaves its column as it is. Archiving an archived category
-- keeps the time it was first archived.
UPDATE categories
SET name                 = COALESCE(sqlc.narg(name), name),
    icon                 = COALESCE(sqlc.narg(icon), icon),
    shade                = COALESCE(sqlc.narg(shade), shade),
    sort_order           = COALESCE(sqlc.narg(sort_order), sort_order),
    monthly_budget_minor = COALESCE(sqlc.narg(monthly_budget_minor), monthly_budget_minor),
    category_type        = COALESCE(sqlc.narg(category_type), category_type),
    archived_at          = CASE
                               WHEN sqlc.narg(archived)::boolean IS NULL THEN archived_at
                               WHEN sqlc.narg(archived)::boolean THEN COALESCE(archived_at, now())
                           END
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
RETURNING *;

-- name: DeleteCategory :execrows
DELETE FROM categories WHERE id = $1 AND user_id = $2;

-- name: ReorderCategories :exec
-- Puts the user's categories in the order of ids.
UPDATE categories c
SET sort_order = (o.position - 1)::integer
FROM unnest(sqlc.arg(ids)::uuid[]) WITH ORDINALITY AS o (id, position)
WHERE c.id = o.id AND c.user_id = sqlc.arg(user_id);

-- name: UpdateCategoryBudgets :exec
-- Sets the budget of ids[i] to amounts[i].
UPDATE categories c
SET monthly_budget_minor = b.amount
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id, unnest(sqlc.arg(amounts)::bigint[]) AS amount) AS b
WHERE c.id = b.id AND c.user_id = sqlc.arg(user_id);
