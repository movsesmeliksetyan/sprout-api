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
