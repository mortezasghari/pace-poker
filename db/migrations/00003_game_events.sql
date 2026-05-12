-- +goose Up
CREATE TABLE game_events (
    id                    BIGSERIAL    PRIMARY KEY,
    game_id               UUID         NOT NULL,
    sequence              BIGINT       NOT NULL,           -- monotonic per game, starts at 1
    event_type            TEXT         NOT NULL,           -- proto message name, e.g. "poker.v1.PlayerFolded"
    payload               BYTEA        NOT NULL,           -- serialized variant message
    envelope              BYTEA        NOT NULL,           -- serialized GameEvent envelope (full event w/ metadata)
    caused_by_command_id  UUID,                            -- nullable: server-initiated events have no command
    hand_id               UUID,                            -- nullable: not all events belong to a hand
    state_version_after   BIGINT       NOT NULL,           -- GameState.version after this event applied
    occurred_at           TIMESTAMPTZ  NOT NULL,
    inserted_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    published_at          TIMESTAMPTZ,                     -- nullable: set when relayed to message bus (future use)

    CONSTRAINT game_events_game_seq_uniq UNIQUE (game_id, sequence),
    CONSTRAINT game_events_sequence_positive CHECK (sequence > 0)
);

-- Primary read path: load events for a game in order.
CREATE INDEX game_events_game_seq_idx ON game_events (game_id, sequence);

-- Per-hand query path: "show me everything that happened in hand X".
CREATE INDEX game_events_hand_idx ON game_events (hand_id, sequence) WHERE hand_id IS NOT NULL;

-- Outbox query path: "give me unpublished events" (future use).
CREATE INDEX game_events_unpublished_idx ON game_events (id) WHERE published_at IS NULL;

-- For idempotency: detect command replays.
CREATE INDEX game_events_command_idx ON game_events (caused_by_command_id) WHERE caused_by_command_id IS NOT NULL;

-- Partitioning by hash(game_id) is commented out — enable when row count justifies it.
-- The current schema is partition-ready: just convert the table and the indexes carry over.

-- +goose Down
DROP TABLE game_events;
