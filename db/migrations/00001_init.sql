-- +goose Up
CREATE EXTENSION IF NOT EXISTS "pgcrypto"; -- for gen_random_uuid()

-- +goose Down
DROP EXTENSION IF EXISTS "pgcrypto";
