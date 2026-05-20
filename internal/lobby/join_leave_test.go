package lobby_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/lobby"
	"github.com/pacepoker/poker/internal/store"
	"github.com/pacepoker/poker/internal/testutil"
)

// ── test helpers ──────────────────────────────────────────────────────────────

func newStore(t *testing.T) store.Store {
	t.Helper()
	pool := testutil.NewPostgresPool(t, "../../db/migrations")
	return store.New(pool)
}

func makeUserWithBalance(t *testing.T, st store.Store, balance int64) (store.User, store.UserSnapshot) {
	t.Helper()
	u, snap, err := st.CreateUser(t.Context(), store.UserInput{DisplayName: "Player"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if balance > 0 {
		_, err = st.CreditUserBalance(t.Context(), u.ID, balance)
		if err != nil {
			t.Fatalf("CreditUserBalance: %v", err)
		}
		snap.ChipBalance = balance
	}
	return u, snap
}

func makeGame(t *testing.T, st store.Store, cfg *pb.CashGameConfig) *pb.GameState {
	t.Helper()
	state, err := st.CreateTable(t.Context(), cfg, uuid.New())
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	return state
}

func defaultCfg() *pb.CashGameConfig {
	return &pb.CashGameConfig{
		TableName:         "Test Table",
		Variant:           pb.GameVariant_GAME_VARIANT_TEXAS_HOLDEM,
		Structure:         pb.BettingStructure_BETTING_STRUCTURE_NO_LIMIT,
		Currency:          "USD",
		SmallBlind:        50,
		BigBlind:          100,
		MinBuyIn:          500,
		MaxBuyIn:          5000,
		MaxSeats:          6,
		MinPlayersToStart: 2,
	}
}

func joinInput(gameID, userID uuid.UUID, buyIn int64, seat int32) store.JoinTableInput {
	return store.JoinTableInput{
		CommandID: uuid.New(),
		GameID:    gameID,
		UserID:    userID,
		Seat:      seat,
		BuyIn:     buyIn,
	}
}

func leaveInput(gameID, userID uuid.UUID) store.LeaveTableInput {
	return store.LeaveTableInput{
		CommandID: uuid.New(),
		GameID:    gameID,
		UserID:    userID,
	}
}

func gameIDFrom(state *pb.GameState) uuid.UUID {
	id, _ := uuid.Parse(state.GetGameId())
	return id
}

// ── happy paths ───────────────────────────────────────────────────────────────

func TestJoinTable_HappyPath(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 10000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	result, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 3))
	if err != nil {
		t.Fatalf("JoinTable: %v", err)
	}
	if result.UserNewBalance != 9500 {
		t.Errorf("balance: got %d want 9500", result.UserNewBalance)
	}
	playerID := u.ID.String()
	p, ok := result.GameState.GetPlayers()[playerID]
	if !ok {
		t.Fatalf("player not in state")
	}
	if p.GetSeat() != 3 {
		t.Errorf("seat: got %d want 3", p.GetSeat())
	}
	if p.GetStack() != 500 {
		t.Errorf("stack: got %d want 500", p.GetStack())
	}
	if result.NewSequence < 2 {
		t.Errorf("sequence should be >= 2, got %d", result.NewSequence)
	}
}

func TestJoinTable_AutoSeat(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 10000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	result, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 0))
	if err != nil {
		t.Fatalf("JoinTable auto-seat: %v", err)
	}
	playerID := u.ID.String()
	p := result.GameState.GetPlayers()[playerID]
	if p.GetSeat() != 1 {
		t.Errorf("auto-seat should be 1, got %d", p.GetSeat())
	}
}

func TestLeaveTable_HappyPath(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 10000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	_, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 1))
	if err != nil {
		t.Fatalf("JoinTable: %v", err)
	}

	result, err := lobby.LeaveTable(t.Context(), st, leaveInput(gid, u.ID))
	if err != nil {
		t.Fatalf("LeaveTable: %v", err)
	}
	if result.CashOutAmount != 500 {
		t.Errorf("cash_out: got %d want 500", result.CashOutAmount)
	}
	if result.UserNewBalance != 10000 {
		t.Errorf("balance after leave: got %d want 10000", result.UserNewBalance)
	}
	if _, ok := result.GameState.GetPlayers()[u.ID.String()]; ok {
		t.Error("player should be removed from state after leave")
	}
}

// ── error / rollback cases ────────────────────────────────────────────────────

func TestJoinTable_InsufficientFunds_NothingChanges(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 50)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	_, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 0))
	if !isErr(err, store.ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}

	// Balance must be unchanged.
	snap, _ := st.GetUserSnapshot(t.Context(), u.ID)
	if snap.ChipBalance != 50 {
		t.Errorf("balance changed despite failure: got %d want 50", snap.ChipBalance)
	}

	// No PlayerJoined event should exist for this game.
	events, _ := st.GetEventsForGame(t.Context(), gid, 2, 10)
	for _, ev := range events {
		if ev.GetPlayerJoined() != nil {
			t.Error("PlayerJoined event was written despite failed join")
		}
	}
}

func TestJoinTable_BuyInBelowMin_NothingChanges(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 1000)
	state := makeGame(t, st, defaultCfg()) // min_buy_in = 500
	gid := gameIDFrom(state)

	_, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 100, 0))
	if !isErr(err, store.ErrBuyInOutOfRange) {
		t.Fatalf("expected ErrBuyInOutOfRange, got %v", err)
	}
	snap, _ := st.GetUserSnapshot(t.Context(), u.ID)
	if snap.ChipBalance != 1000 {
		t.Errorf("balance changed: got %d want 1000", snap.ChipBalance)
	}
}

func TestJoinTable_SeatTaken_NothingChanges(t *testing.T) {
	st := newStore(t)
	u1, _ := makeUserWithBalance(t, st, 1000)
	u2, _ := makeUserWithBalance(t, st, 1000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	_, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u1.ID, 500, 3))
	if err != nil {
		t.Fatalf("p1 join: %v", err)
	}

	_, err = lobby.JoinTable(t.Context(), st, joinInput(gid, u2.ID, 500, 3))
	if !isErr(err, store.ErrSeatTaken) {
		t.Fatalf("expected ErrSeatTaken, got %v", err)
	}
	snap, _ := st.GetUserSnapshot(t.Context(), u2.ID)
	if snap.ChipBalance != 1000 {
		t.Errorf("p2 balance changed: got %d want 1000", snap.ChipBalance)
	}
}

func TestJoinTable_AlreadySeated_NothingChanges(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 2000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	_, err := lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 0))
	if err != nil {
		t.Fatalf("first join: %v", err)
	}

	_, err = lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 0))
	if !isErr(err, store.ErrAlreadySeated) {
		t.Fatalf("expected ErrAlreadySeated, got %v", err)
	}
	snap, _ := st.GetUserSnapshot(t.Context(), u.ID)
	if snap.ChipBalance != 1500 {
		t.Errorf("balance after dupe join attempt: got %d want 1500", snap.ChipBalance)
	}
}

func TestJoinTable_IdempotentByCommandID(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 1000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	in := joinInput(gid, u.ID, 500, 0)
	r1, err := lobby.JoinTable(t.Context(), st, in)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}

	r2, err := lobby.JoinTable(t.Context(), st, in) // same CommandID
	if err != nil {
		t.Fatalf("idempotent join: %v", err)
	}
	if r2.NewSequence != r1.NewSequence {
		t.Errorf("sequence mismatch: %d != %d", r2.NewSequence, r1.NewSequence)
	}

	// Balance should have been debited exactly once.
	snap, _ := st.GetUserSnapshot(t.Context(), u.ID)
	if snap.ChipBalance != 500 {
		t.Errorf("balance debited twice: got %d want 500", snap.ChipBalance)
	}
}

func TestJoinTable_ConcurrentSeatGrab(t *testing.T) {
	st := newStore(t)
	u1, _ := makeUserWithBalance(t, st, 1000)
	u2, _ := makeUserWithBalance(t, st, 1000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	var (
		wg      sync.WaitGroup
		errs    [2]error
		results [2]*store.JoinTableResult
	)
	for i, u := range []uuid.UUID{u1.ID, u2.ID} {
		wg.Add(1)
		go func(idx int, uid uuid.UUID) {
			defer wg.Done()
			results[idx], errs[idx] = lobby.JoinTable(
				context.Background(), st,
				joinInput(gid, uid, 500, 1), // both request seat 1
			)
		}(i, u)
	}
	wg.Wait()

	successes := 0
	for i := range 2 {
		if errs[i] == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (errs: %v, %v)", successes, errs[0], errs[1])
	}
}

func TestLeaveTable_NotAtTable(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 1000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	_, err := lobby.LeaveTable(t.Context(), st, leaveInput(gid, u.ID))
	if !isErr(err, store.ErrUserNotAtTable) {
		t.Fatalf("expected ErrUserNotAtTable, got %v", err)
	}
}

func TestLeaveTable_IdempotentByCommandID(t *testing.T) {
	st := newStore(t)
	u, _ := makeUserWithBalance(t, st, 1000)
	state := makeGame(t, st, defaultCfg())
	gid := gameIDFrom(state)

	_, _ = lobby.JoinTable(t.Context(), st, joinInput(gid, u.ID, 500, 0))

	in := leaveInput(gid, u.ID)
	r1, err := lobby.LeaveTable(t.Context(), st, in)
	if err != nil {
		t.Fatalf("first leave: %v", err)
	}

	_, err = lobby.LeaveTable(t.Context(), st, in) // same CommandID
	if err != nil {
		t.Fatalf("idempotent leave: %v", err)
	}

	// Balance credited exactly once.
	snap, _ := st.GetUserSnapshot(t.Context(), u.ID)
	if snap.ChipBalance != r1.UserNewBalance {
		t.Errorf("balance after idempotent leave: got %d want %d", snap.ChipBalance, r1.UserNewBalance)
	}
	if snap.ChipBalance != 1000 {
		t.Errorf("expected full refund 1000, got %d", snap.ChipBalance)
	}
}

// TestJoinTable_AppendEventFailureRollsBackBalance is the critical atomicity
// test: wrap the store so AppendEvent returns ErrConcurrentWrite, then verify
// that the user's balance debit was rolled back.
func TestJoinTable_AppendEventFailureRollsBackBalance(t *testing.T) {
	realSt := newStore(t)
	u, _ := makeUserWithBalance(t, realSt, 1000)
	gameState := makeGame(t, realSt, defaultCfg())
	gid := gameIDFrom(gameState)

	// Wrap the store: AppendEvent inside the tx always returns ErrConcurrentWrite.
	// DebitUserBalance runs on the real tx first, but the tx is rolled back when
	// the function returns an error — verifying atomicity.
	failSt := &appendFailStore{Store: realSt}

	_, err := lobby.JoinTable(t.Context(), failSt, store.JoinTableInput{
		CommandID: uuid.New(),
		GameID:    gid,
		UserID:    u.ID,
		Seat:      0,
		BuyIn:     500,
	})
	if !isErr(err, store.ErrConcurrentWrite) {
		t.Fatalf("expected ErrConcurrentWrite, got %v", err)
	}

	// Critical assertion: balance must be unchanged.
	snap, err2 := realSt.GetUserSnapshot(t.Context(), u.ID)
	if err2 != nil {
		t.Fatalf("GetUserSnapshot: %v", err2)
	}
	if snap.ChipBalance != 1000 {
		t.Errorf("ATOMICITY BROKEN: balance changed to %d despite failed join", snap.ChipBalance)
	}

	// No PlayerJoined event should exist for this user.
	events, _ := realSt.GetEventsForGame(t.Context(), gid, 1, 100)
	for _, ev := range events {
		if pj := ev.GetPlayerJoined(); pj != nil && pj.GetUserId() == u.ID.String() {
			t.Error("PlayerJoined event was persisted despite transaction rollback")
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// appendFailStore wraps a Store and returns ErrConcurrentWrite from every
// AppendEvent call made inside a transaction. All other methods delegate to
// the real store, so DebitUserBalance etc. run for real and are then rolled
// back when the function returns an error — this verifies tx atomicity.
type appendFailStore struct{ store.Store }

func (w *appendFailStore) WithTx(ctx context.Context, fn func(store.Store) error) error {
	return w.Store.WithTx(ctx, func(tx store.Store) error {
		return fn(&appendFailTxStore{Store: tx})
	})
}

type appendFailTxStore struct{ store.Store }

func (f *appendFailTxStore) AppendEvent(_ context.Context, _ *pb.GameEvent) error {
	return store.ErrConcurrentWrite
}

func isErr(err, target error) bool {
	if err == nil {
		return false
	}
	// errors.Is handles wrapped errors.
	return err == target || errContains(err, target)
}

func errContains(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
