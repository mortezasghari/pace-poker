-- +goose Up
CREATE TABLE game_configs (
    game_id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    table_name        TEXT         NOT NULL,
    variant           SMALLINT     NOT NULL,   -- enum value from proto
    structure         SMALLINT     NOT NULL,   -- enum value from proto
    currency          TEXT         NOT NULL,   -- ISO 4217
    small_blind       BIGINT       NOT NULL,
    big_blind         BIGINT       NOT NULL,
    min_buy_in        BIGINT       NOT NULL,
    max_buy_in        BIGINT       NOT NULL,   -- 0 = uncapped
    max_seats         SMALLINT     NOT NULL,
    is_private        BOOLEAN      NOT NULL DEFAULT FALSE,
    config_proto      BYTEA        NOT NULL,   -- serialized CashGameConfig
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ              -- soft delete
);

CREATE INDEX game_configs_active_idx
    ON game_configs (created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX game_configs_stakes_idx
    ON game_configs (variant, structure, big_blind)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE game_configs;
