package store

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver for database/sql (goose only)
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	testPool  *pgxpool.Pool
	testStore Store
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("poker"),
		tcpostgres.WithUsername("poker"),
		tcpostgres.WithPassword("poker"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}
	defer pgContainer.Terminate(ctx) //nolint:errcheck

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get connection string: %v", err)
	}

	// goose requires database/sql; pgx stdlib driver is registered via blank import above.
	sqlDB, err := sql.Open("pgx", connStr)
	if err != nil {
		log.Fatalf("open sql db: %v", err)
	}
	defer sqlDB.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("goose set dialect: %v", err)
	}
	if err := goose.Up(sqlDB, "../../db/migrations"); err != nil {
		log.Fatalf("goose up: %v", err)
	}

	testPool, err = pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatalf("create pool: %v", err)
	}
	defer testPool.Close()

	testStore = New(testPool)
	return m.Run()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func truncate(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		"TRUNCATE game_configs, game_events, game_snapshots CASCADE")
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}

func makeEvent(t *testing.T, gameID string, seq uint64, stateVer int64) *pb.GameEvent {
	t.Helper()
	return &pb.GameEvent{
		GameId:            gameID,
		Sequence:          seq,
		OccurredAt:        timestamppb.Now(),
		StateVersion:      stateVer,
		CausedByCommandId: uuid.New().String(),
		Event: &pb.GameEvent_PlayerFolded{
			PlayerFolded: &pb.PlayerFolded{PlayerId: "player-1"},
		},
	}
}

func makeConfig() *pb.CashGameConfig {
	return &pb.CashGameConfig{
		TableName:  "Test Table",
		Variant:    pb.GameVariant_GAME_VARIANT_TEXAS_HOLDEM,
		Structure:  pb.BettingStructure_BETTING_STRUCTURE_NO_LIMIT,
		Currency:   "USD",
		SmallBlind: 50,
		BigBlind:   100,
		MinBuyIn:   5000,
		MaxBuyIn:   50000,
		MaxSeats:   9,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestCreateAndGetGameConfig(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	cfg := makeConfig()
	gameID, err := testStore.CreateGameConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateGameConfig: %v", err)
	}
	if gameID == uuid.Nil {
		t.Fatal("expected non-nil game ID")
	}

	got, err := testStore.GetGameConfig(ctx, gameID)
	if err != nil {
		t.Fatalf("GetGameConfig: %v", err)
	}
	if got.GetTableName() != cfg.GetTableName() {
		t.Errorf("table_name: got %q, want %q", got.GetTableName(), cfg.GetTableName())
	}
	if got.GetBigBlind() != cfg.GetBigBlind() {
		t.Errorf("big_blind: got %d, want %d", got.GetBigBlind(), cfg.GetBigBlind())
	}
	if got.GetVariant() != cfg.GetVariant() {
		t.Errorf("variant: got %v, want %v", got.GetVariant(), cfg.GetVariant())
	}
	if got.GetCurrency() != cfg.GetCurrency() {
		t.Errorf("currency: got %q, want %q", got.GetCurrency(), cfg.GetCurrency())
	}
}

func TestAppendAndReadEvents(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()

	for seq := uint64(1); seq <= 5; seq++ {
		if err := testStore.AppendEvent(ctx, makeEvent(t, gameID, seq, int64(seq))); err != nil {
			t.Fatalf("AppendEvent seq=%d: %v", seq, err)
		}
	}

	id, _ := uuid.Parse(gameID)
	evts, err := testStore.GetEventsForGame(ctx, id, 1, 10)
	if err != nil {
		t.Fatalf("GetEventsForGame: %v", err)
	}
	if len(evts) != 5 {
		t.Fatalf("expected 5 events, got %d", len(evts))
	}
	for i, evt := range evts {
		if want := uint64(i + 1); evt.GetSequence() != want {
			t.Errorf("event[%d]: sequence = %d, want %d", i, evt.GetSequence(), want)
		}
	}
}

func TestAppendEventConcurrentWriteRejected(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()

	if err := testStore.AppendEvent(ctx, makeEvent(t, gameID, 5, 5)); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	err := testStore.AppendEvent(ctx, makeEvent(t, gameID, 5, 6))
	if !errors.Is(err, ErrConcurrentWrite) {
		t.Errorf("expected ErrConcurrentWrite, got %v", err)
	}
}

func TestAppendEventsBatchAtomic(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()
	id, _ := uuid.Parse(gameID)

	// Third event duplicates sequence 2 — entire batch must roll back.
	evts := []*pb.GameEvent{
		makeEvent(t, gameID, 1, 1),
		makeEvent(t, gameID, 2, 2),
		makeEvent(t, gameID, 2, 3), // duplicate sequence
	}

	err := testStore.AppendEvents(ctx, evts)
	if !errors.Is(err, ErrConcurrentWrite) {
		t.Errorf("expected ErrConcurrentWrite, got %v", err)
	}

	rows, err := testStore.GetEventsForGame(ctx, id, 1, 10)
	if err != nil {
		t.Fatalf("GetEventsForGame after rollback: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 events after rollback, got %d", len(rows))
	}
}

func TestFindEventByCommandID(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()
	cmdID := uuid.New().String()

	evt := makeEvent(t, gameID, 1, 1)
	evt.CausedByCommandId = cmdID
	if err := testStore.AppendEvent(ctx, evt); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	cmdUUID, _ := uuid.Parse(cmdID)
	found, err := testStore.FindEventByCommandID(ctx, cmdUUID)
	if err != nil {
		t.Fatalf("FindEventByCommandID: %v", err)
	}
	if found.GetCausedByCommandId() != cmdID {
		t.Errorf("command_id: got %q, want %q", found.GetCausedByCommandId(), cmdID)
	}
}

func TestCreateAndGetSnapshot(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()
	id, _ := uuid.Parse(gameID)

	state := &pb.GameState{
		GameId:  gameID,
		Version: 42,
		Status:  pb.GameStatus_GAME_STATUS_ACTIVE,
	}
	if err := testStore.CreateSnapshot(ctx, state, 3); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	got, seq, err := testStore.GetLatestSnapshot(ctx, id)
	if err != nil {
		t.Fatalf("GetLatestSnapshot: %v", err)
	}
	if seq != 42 {
		t.Errorf("sequence: got %d, want 42", seq)
	}
	if got.GetVersion() != state.GetVersion() {
		t.Errorf("version: got %d, want %d", got.GetVersion(), state.GetVersion())
	}
	if got.GetStatus() != state.GetStatus() {
		t.Errorf("status: got %v, want %v", got.GetStatus(), state.GetStatus())
	}
}

func TestGetSnapshotAtOrBefore(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()
	id, _ := uuid.Parse(gameID)

	for _, ver := range []int64{10, 20, 30} {
		state := &pb.GameState{GameId: gameID, Version: ver}
		if err := testStore.CreateSnapshot(ctx, state, ver/10); err != nil {
			t.Fatalf("CreateSnapshot ver=%d: %v", ver, err)
		}
	}

	got, seq, err := testStore.GetSnapshotAtOrBefore(ctx, id, 25)
	if err != nil {
		t.Fatalf("GetSnapshotAtOrBefore(25): %v", err)
	}
	if seq != 20 {
		t.Errorf("expected snapshot at seq=20, got seq=%d", seq)
	}
	if got.GetVersion() != 20 {
		t.Errorf("state.version: got %d, want 20", got.GetVersion())
	}
}

func TestWithTxRollback(t *testing.T) {
	truncate(t)
	ctx := context.Background()
	gameID := uuid.New().String()
	id, _ := uuid.Parse(gameID)

	sentinel := errors.New("rollback trigger")

	err := testStore.WithTx(ctx, func(tx Store) error {
		if err := tx.AppendEvent(ctx, makeEvent(t, gameID, 1, 1)); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}

	rows, err := testStore.GetEventsForGame(ctx, id, 1, 10)
	if err != nil {
		t.Fatalf("GetEventsForGame after rollback: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 events after rollback, got %d", len(rows))
	}
}