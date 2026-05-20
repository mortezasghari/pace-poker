-- name: CreateUserSnapshot :one
INSERT INTO user_snapshots (user_id)
VALUES ($1)
RETURNING *;

-- name: GetUserSnapshot :one
SELECT * FROM user_snapshots WHERE user_id = $1;

-- name: GetUserSnapshotForUpdate :one
SELECT * FROM user_snapshots WHERE user_id = $1 FOR UPDATE;

-- name: ApplyDepositToSnapshot :one
UPDATE user_snapshots
SET
    chip_balance     = chip_balance + $2,
    total_deposited  = total_deposited + $2,
    total_reports    = total_reports + 1,
    last_deposit_at  = CASE WHEN $2 > 0 THEN NOW() ELSE last_deposit_at END,
    last_activity_at = NOW(),
    updated_at       = NOW()
WHERE user_id = $1
RETURNING *;

-- name: DebitUserBalance :one
-- For buy-ins when a user joins a table. Atomic insufficient-funds check:
-- if balance < amount the WHERE clause matches nothing and ErrNoRows is returned.
UPDATE user_snapshots
SET
    chip_balance     = chip_balance - $2,
    last_activity_at = NOW(),
    updated_at       = NOW()
WHERE user_id = $1 AND chip_balance >= $2
RETURNING *;

-- name: CreditUserBalance :one
-- For cash-out (returning chips when a user leaves a table).
UPDATE user_snapshots
SET
    chip_balance     = chip_balance + $2,
    last_activity_at = NOW(),
    updated_at       = NOW()
WHERE user_id = $1
RETURNING *;
