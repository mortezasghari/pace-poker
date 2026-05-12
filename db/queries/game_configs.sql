-- name: CreateGameConfig :one
INSERT INTO game_configs (
    table_name, variant, structure, currency,
    small_blind, big_blind, min_buy_in, max_buy_in,
    max_seats, is_private, config_proto
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetGameConfig :one
SELECT * FROM game_configs WHERE game_id = $1 AND deleted_at IS NULL;

-- name: ListActiveGameConfigs :many
SELECT * FROM game_configs
WHERE deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: SoftDeleteGameConfig :exec
UPDATE game_configs SET deleted_at = NOW(), updated_at = NOW()
WHERE game_id = $1 AND deleted_at IS NULL;

-- name: SearchGameConfigs :many
-- Lobby search. All filters are optional and AND-combined.
-- Filtering on denormalized columns rather than decoding config_proto.
-- Sentinels: name_query='' means no name filter; 0 means no numeric filter.
SELECT
    game_id, table_name, variant, structure, currency,
    small_blind, big_blind, min_buy_in, max_buy_in,
    max_seats, is_private, created_at
FROM game_configs
WHERE deleted_at IS NULL
  AND ($1::TEXT = ''    OR table_name ILIKE '%' || $1::TEXT || '%')
  AND ($2::BIGINT = 0   OR big_blind  = $2::BIGINT)
  AND ($3::SMALLINT = 0 OR variant    = $3::SMALLINT)
  AND ($4::SMALLINT = 0 OR structure  = $4::SMALLINT)
ORDER BY created_at DESC
LIMIT $5 OFFSET $6;

-- name: CountSearchGameConfigs :one
SELECT COUNT(*)::BIGINT AS total
FROM game_configs
WHERE deleted_at IS NULL
  AND ($1::TEXT = ''    OR table_name ILIKE '%' || $1::TEXT || '%')
  AND ($2::BIGINT = 0   OR big_blind  = $2::BIGINT)
  AND ($3::SMALLINT = 0 OR variant    = $3::SMALLINT)
  AND ($4::SMALLINT = 0 OR structure  = $4::SMALLINT);
