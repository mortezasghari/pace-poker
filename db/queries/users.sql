-- name: CreateUser :one
INSERT INTO users (id, display_name, external_id, timezone, max_daily_deposit)
VALUES ($1, $2, NULLIF($3, ''), COALESCE(NULLIF($4, ''), 'UTC'), $5)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByExternalID :one
SELECT * FROM users WHERE external_id = $1 AND deleted_at IS NULL;

-- name: UpdateUserSettings :one
UPDATE users
SET
    display_name      = COALESCE(NULLIF($2, ''), display_name),
    timezone          = COALESCE(NULLIF($3, ''), timezone),
    max_daily_deposit = CASE WHEN $4::BIGINT < 0 THEN max_daily_deposit ELSE $4 END,
    updated_at        = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;
