-- +goose Up

-- The per-user-per-day high-water mark. One row per (user, local_date).
-- Updated atomically via INSERT ... ON CONFLICT DO UPDATE.
CREATE TABLE user_daily_steps (
    user_id          UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    local_date       DATE         NOT NULL,                 -- date in user's local timezone
    max_steps        BIGINT       NOT NULL,                 -- highest cumulative count seen today
    last_reported_at TIMESTAMPTZ  NOT NULL,                 -- when the high-water mark was last updated
    PRIMARY KEY (user_id, local_date),
    CONSTRAINT user_daily_steps_nonneg CHECK (max_steps >= 0)
);

-- The audit log: every deposit report, including no-ops.
-- Append-only. Used for debugging, fraud analysis, and replay.
CREATE TABLE step_deposit_reports (
    id              BIGSERIAL    PRIMARY KEY,
    user_id         UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    report_id       UUID         NOT NULL,                  -- client-supplied idempotency key
    reported_steps  BIGINT       NOT NULL,                  -- the count the client sent
    credited_chips  BIGINT       NOT NULL DEFAULT 0,        -- 0 if no-op
    local_date      DATE         NOT NULL,
    reported_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),    -- when the server received it
    reason          TEXT         NOT NULL,                  -- 'NEW_DAY', 'INCREMENT', 'NO_OP_LOWER', etc.

    CONSTRAINT step_deposit_reports_report_uniq       UNIQUE (user_id, report_id),
    CONSTRAINT step_deposit_reports_credited_nonneg   CHECK (credited_chips >= 0)
);

CREATE INDEX step_deposit_reports_user_date_idx
    ON step_deposit_reports (user_id, local_date DESC, reported_at DESC);

-- +goose Down
DROP TABLE step_deposit_reports;
DROP TABLE user_daily_steps;
