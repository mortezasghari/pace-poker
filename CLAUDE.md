# poker-go — Claude context

## Project purpose

A production-ready No-Limit Texas Hold'em **cash-game** server written in Go,
exposed over gRPC + Protobuf. The goal is a correct event-sourced domain model
that can later be backed by real storage, a real dealer engine, and real money.

## Proto layout

```text
proto/poker/v1/
  common.proto    — shared enums, card encoding constants
  config.proto    — CashGameConfig (immutable table config)
  state.proto     — PlayerState, HandState, GameState (mutable snapshots)
  events.proto    — GameEvent envelope + all event variant messages
  commands.proto  — PlayerCommand envelope + all command messages + lobby/admin pairs
  service.proto   — PokerService gRPC service definition
```

Generated Go code lives in `gen/go/poker/v1/` — **do not edit files there by hand**.

To regenerate after editing protos:

```sh
make generate   # runs: buf generate
make lint       # runs: buf lint
make build      # runs: generate then go build ./...
```

## Card encoding

Cards are `uint32` on the wire (proto3 has no uint8).

```text
card = suit * 13 + rank
rank ∈ [0..12]  →  2, 3, 4, 5, 6, 7, 8, 9, 10, J, Q, K, A
suit ∈ [0..3]   →  ♣ (Clubs), ♦ (Diamonds), ♥ (Hearts), ♠ (Spades)
```

Examples: `0` = 2♣, `12` = A♣, `13` = 2♦, `51` = A♠.

Sentinel for "no card": `255` (`CardConstants_Sentinel_NO_CARD`).

In `HandState`/`PlayerState`, hole/board cards are `optional uint32` — presence
is meaningful. In event messages, cards are plain `uint32` (the event firing
implies the card exists). Five-card winning hands in `PotAwarded.winning_hand`
are `bytes` (exactly 5 bytes, each a card value).

## Commands are intent, events are facts

- A **command** (`PlayerCommand`, `CreateTableCommand`, …) expresses what a
  player or admin *wants* to do. It may be rejected.
- An **event** (`GameEvent` variants) records what *actually happened*. Events
  are never revised once emitted.
- Every successful command produces one or more events. A rejected command
  produces a single `CommandRejected` event.

## Sequence numbers

Every `GameEvent` carries a `sequence uint64` that is monotonically increasing
**per game**. Sequence numbers never skip or reset. Clients use gaps in the
sequence to detect missed events and request a replay via `StreamGameEvents`
with `from_sequence`.

## Adding new event or command types

1. Add the new message to `events.proto` or `commands.proto`.
2. Add a new `oneof` field to `GameEvent.event` or `PlayerCommand.payload`.
   Use the next available field number — do not reuse retired numbers.
3. Run `make generate` and `make lint`.
4. Add a case to `dispatchCommand` in `internal/server/session.go`.
5. Name events in past tense (`PlayerFolded`, `HandEnded`).
   Name commands in imperative (`FoldCommand`, `BetCommand`).

## Never edit gen/

The `gen/` directory is committed codegen output. Editing it by hand will be
overwritten on the next `make generate` and will cause `buf breaking` to fail.

## Database layer

**Stack:** `pgx/v5` + `pgxpool` for connections, `sqlc` for query codegen, `goose` for migrations.

**Layout:**

```text
db/migrations/          — goose SQL migration files (NNNNN_short_name.sql)
db/queries/             — sqlc input SQL files
db/sqlc.yaml            — sqlc config
internal/store/pgstore/ — generated sqlc Go code (do not edit by hand)
internal/store/store.go — Store interface + pgStore implementation
```

To regenerate after editing queries:

```sh
make sqlc        # cd db && sqlc generate
```

To run migrations against the local DB:

```sh
make db-up       # docker compose up -d postgres
make migrate-up  # goose up
```

**Event payloads are protobuf bytes.** Never JSON in the database. The `envelope`
column holds the full serialized `GameEvent`; `payload` holds only the inner variant
message bytes. Both are `BYTEA`.

**Optimistic concurrency:** `UNIQUE (game_id, sequence)` on `game_events` is the
OCC lock. Inserting the next event specifies its sequence; a duplicate sequence
means a concurrent writer got there first. The store catches `pgerrcode.UniqueViolation`
(23505) and returns `store.ErrConcurrentWrite` — callers refresh state and retry.

**Snapshot strategy:** snapshots are taken between hands with `hand_number` recorded.
Replay = latest snapshot at or before target sequence + all events with sequence >
snapshot.sequence. Use `GetSnapshotAtOrBefore` + `GetEventsForGame`.

**Migration naming:** `NNNNN_short_name.sql` with `-- +goose Up` and `-- +goose Down`
sections. Do not reuse migration numbers.

## Server layer conventions

**`CreateTable` pattern:** every successful command writes atomically via `store.WithTx`:
`CreateGameConfig` → `AppendEvent(TableCreated, seq=1)` → `CreateSnapshot(version=1)`.
All future command handlers must follow this same atomic write pattern.

**`command_id` idempotency:** every lobby/admin command carries a `command_id` UUID.
If the server has already processed that command (found via `FindEventByCommandID`),
it returns the stored snapshot rather than executing again. Empty `command_id` means
the client opted out of idempotency — the server generates a fresh UUID so the event
log is always fully attributed, but no dedup check is performed.

**Limit clamping in `SearchTables`:** `limit ≤ 0` → 50 (default), `limit > 200` → 200
(max). These bounds are enforced in the server handler, not in SQL, so the generated
query type always receives a sane value.

**Validation boundary:** `validateCashGameConfig` in `internal/server/validate.go` is
the sole place config rules live. The store accepts whatever the server passes — do not
add business-rule checks inside pgStore methods.

## Engine layer (actor pattern)

The engine layer (`internal/engine`) implements game logic using the actor pattern:

- **Session**: one per active game. Owns the game's `GameState`. A single goroutine
  inside the Session is the only code that reads or writes the state. External
  callers send commands via `Submit`, which posts to the Session's inbox channel.
- **Router**: maps game IDs to Sessions. Lazy-loads sessions from the store
  (snapshot + events) on first access. Evicts idle sessions to free memory.

Rules for working in this package:

1. NEVER add a mutex to `GameState`. If you find yourself wanting one, you're
   touching state from outside the run goroutine — refactor instead.
2. `handleCommand` and `applyEvents` are pure functions. They must not call the
   store or do I/O. The Session orchestrates persistence around them.
3. Persist events BEFORE applying them to in-memory state. A DB failure
   must not leave in-memory state ahead of the persisted log.
4. `Submit` is safe to call from many goroutines. The Session serializes them.
   You should not need `sync.RWMutex`, `sync.Map`, or `sync/atomic` at this layer.
