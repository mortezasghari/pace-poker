-- +goose Up
CREATE TABLE user_snapshots (
    user_id           UUID         PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    chip_balance      BIGINT       NOT NULL DEFAULT 0,
    total_deposited   BIGINT       NOT NULL DEFAULT 0,    -- lifetime sum of credited chips
    total_reports     BIGINT       NOT NULL DEFAULT 0,    -- lifetime count of deposit reports (incl. no-ops)
    last_deposit_at   TIMESTAMPTZ,                        -- last non-zero credit
    last_activity_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),-- any user-touching action
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT user_snapshots_balance_nonneg    CHECK (chip_balance >= 0),
    CONSTRAINT user_snapshots_deposited_nonneg  CHECK (total_deposited >= 0)
);

-- +goose Down
DROP TABLE user_snapshots;
