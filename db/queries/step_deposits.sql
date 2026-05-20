-- name: GetDailyStepsForUpdate :one
-- Row-level lock for the deposit transaction.
SELECT * FROM user_daily_steps
WHERE user_id = $1 AND local_date = $2
FOR UPDATE;

-- name: UpsertDailyStepsHighWaterMark :one
-- Atomically update only if the new value is higher.
INSERT INTO user_daily_steps (user_id, local_date, max_steps, last_reported_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (user_id, local_date) DO UPDATE
    SET max_steps        = GREATEST(user_daily_steps.max_steps, EXCLUDED.max_steps),
        last_reported_at = CASE
            WHEN EXCLUDED.max_steps > user_daily_steps.max_steps THEN NOW()
            ELSE user_daily_steps.last_reported_at
        END
RETURNING *;

-- name: InsertDepositReport :one
-- Records the audit row. Returns the inserted row; on conflict (duplicate
-- report_id) the DO NOTHING clause fires and nothing is returned.
INSERT INTO step_deposit_reports (user_id, report_id, reported_steps, credited_chips, local_date, reason)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, report_id) DO NOTHING
RETURNING *;

-- name: FindDepositReportByReportID :one
SELECT * FROM step_deposit_reports
WHERE user_id = $1 AND report_id = $2;

-- name: ListDepositReports :many
SELECT * FROM step_deposit_reports
WHERE user_id = $1
ORDER BY reported_at DESC
LIMIT $2 OFFSET $3;

-- name: CountDepositReports :one
SELECT COUNT(*)::BIGINT FROM step_deposit_reports WHERE user_id = $1;

-- name: SumCreditedToday :one
SELECT COALESCE(SUM(credited_chips), 0)::BIGINT
FROM step_deposit_reports
WHERE user_id = $1 AND local_date = $2;
