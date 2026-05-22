-- name: AppendGameEvent :one
-- Inserts a single event. Relies on UNIQUE (game_id, sequence) for OCC.
-- Caller catches "unique_violation" (pgerrcode 23505) and retries with refreshed state.
INSERT INTO game_events (
    game_id, sequence, event_type, payload, envelope,
    caused_by_command_id, hand_id, state_version_after, occurred_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: AppendGameEvents :copyfrom
-- Bulk variant for when a single command produces multiple events.
-- Uses pgx's COPY FROM for speed. Atomicity is guaranteed by wrapping in a transaction.
INSERT INTO game_events (
    game_id, sequence, event_type, payload, envelope,
    caused_by_command_id, hand_id, state_version_after, occurred_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetEventsForGame :many
SELECT * FROM game_events
WHERE game_id = $1 AND sequence >= $2
ORDER BY sequence ASC
LIMIT $3;

-- name: GetLatestSequence :one
SELECT COALESCE(MAX(sequence), 0)::BIGINT AS latest_sequence
FROM game_events
WHERE game_id = $1;

-- name: FindEventByCommandID :one
-- Idempotency check: was this command already processed for this specific game?
SELECT * FROM game_events
WHERE caused_by_command_id = $1
  AND game_id = $2
ORDER BY sequence ASC
LIMIT 1;

-- name: FindEventByCommandIDGlobal :one
-- Idempotency check for commands that precede game creation (e.g. CreateTable),
-- where the game_id is not yet known at idempotency-check time.
SELECT * FROM game_events
WHERE caused_by_command_id = $1
ORDER BY sequence ASC
LIMIT 1;

-- name: GetEventsForHand :many
SELECT * FROM game_events
WHERE hand_id = $1
ORDER BY sequence ASC;

-- name: MarkEventsPublished :exec
UPDATE game_events
SET published_at = NOW()
WHERE id = ANY($1::BIGINT[]);

-- name: GetUnpublishedEvents :many
SELECT * FROM game_events
WHERE published_at IS NULL
ORDER BY id ASC
LIMIT $1;
