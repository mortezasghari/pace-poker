-- +goose Up
CREATE TABLE game_snapshots (
    id              BIGSERIAL    PRIMARY KEY,
    game_id         UUID         NOT NULL,
    sequence        BIGINT       NOT NULL,  -- the GameState.version captured
    hand_number     BIGINT       NOT NULL,  -- which hand had just ended (0 = before any hand)
    state_proto     BYTEA        NOT NULL,  -- serialized GameState
    taken_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT game_snapshots_game_seq_uniq UNIQUE (game_id, sequence)
);

-- Find the latest snapshot for a game (or latest <= a target sequence) cheaply.
CREATE INDEX game_snapshots_game_seq_idx ON game_snapshots (game_id, sequence DESC);

-- +goose Down
DROP TABLE game_snapshots;
