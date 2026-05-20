-- +goose Up
CREATE TABLE users (
    id                UUID         PRIMARY KEY,
    display_name      TEXT         NOT NULL,
    external_id       TEXT,                              -- nullable; populated when OAuth wires in
    timezone          TEXT         NOT NULL DEFAULT 'UTC',
    max_daily_deposit BIGINT       NOT NULL DEFAULT 0,   -- 0 = uncapped
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ,

    CONSTRAINT users_external_id_uniq  UNIQUE (external_id),
    CONSTRAINT users_display_name_len  CHECK (char_length(display_name) BETWEEN 1 AND 50),
    CONSTRAINT users_max_daily_nonneg  CHECK (max_daily_deposit >= 0)
);

-- For external_id lookup (the OAuth path).
CREATE INDEX users_external_id_idx ON users (external_id) WHERE external_id IS NOT NULL;

-- +goose Down
DROP TABLE users;
