-- +goose Up
-- Case-insensitive substring search on table_name.
-- At lobby scale (thousands of rows, not millions), this is fine.
-- If table count grows past ~100k, consider adding pg_trgm + GIN index.
CREATE INDEX game_configs_name_idx
    ON game_configs (LOWER(table_name))
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX game_configs_name_idx;
