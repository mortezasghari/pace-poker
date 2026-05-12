-- name: CreateSnapshot :one
INSERT INTO game_snapshots (game_id, sequence, hand_number, state_proto)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLatestSnapshot :one
SELECT * FROM game_snapshots
WHERE game_id = $1
ORDER BY sequence DESC
LIMIT 1;

-- name: GetSnapshotAtOrBefore :one
-- For replay: find the most recent snapshot at or before a target sequence.
SELECT * FROM game_snapshots
WHERE game_id = $1 AND sequence <= $2
ORDER BY sequence DESC
LIMIT 1;

-- name: DeleteSnapshotsBefore :exec
-- Snapshot pruning. Keep N most recent snapshots per game.
DELETE FROM game_snapshots
WHERE game_id = $1 AND sequence < $2;
