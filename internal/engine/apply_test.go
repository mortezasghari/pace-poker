package engine

import (
	"testing"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"google.golang.org/protobuf/proto"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func baseState() *pb.GameState {
	return &pb.GameState{
		GameId:  "game-1",
		Status:  pb.GameStatus_GAME_STATUS_WAITING,
		Version: 1,
		Config: &pb.CashGameConfig{
			SmallBlind: 50,
			BigBlind:   100,
		},
		Players: map[string]*pb.PlayerState{
			"p1": {PlayerId: "p1", Seat: 0, Stack: 1000},
			"p2": {PlayerId: "p2", Seat: 1, Stack: 1000},
		},
	}
}

func handState(dealerID, sbID, bbID string) *pb.HandState {
	return &pb.HandState{
		HandId:             "hand-1",
		DealerPlayerId:     dealerID,
		SmallBlindPlayerId: sbID,
		BigBlindPlayerId:   bbID,
		Phase:              pb.HandPhase_HAND_PHASE_PREFLOP,
		CurrentHighestBet:  100,
		MinRaiseAmount:     100,
		Pot:                150,
		ActionOrder:        []string{"p1", "p2"},
		PendingActions:     []string{"p1"},
		ActingPlayerId:     ptr("p1"),
	}
}

func ptr(s string) *string { return &s }

func mustApply(t *testing.T, state *pb.GameState, evt *pb.GameEvent) *pb.GameState {
	t.Helper()
	return apply(state, evt)
}

// ── immutability ──────────────────────────────────────────────────────────────

func TestApply_DoesNotMutateInput(t *testing.T) {
	state := baseState()
	original := proto.Clone(state).(*pb.GameState)

	apply(state, &pb.GameEvent{Event: &pb.GameEvent_TableClosed{TableClosed: &pb.TableClosed{}}})

	if !proto.Equal(state, original) {
		t.Error("apply mutated the input state")
	}
}

// ── table lifecycle ───────────────────────────────────────────────────────────

func TestApply_TableClosed(t *testing.T) {
	state := baseState()
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_TableClosed{TableClosed: &pb.TableClosed{}}})
	if next.Status != pb.GameStatus_GAME_STATUS_CLOSED {
		t.Errorf("Status: got %v, want CLOSED", next.Status)
	}
}

func TestApply_TablePaused(t *testing.T) {
	state := baseState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_TablePaused{TablePaused: &pb.TablePaused{}}})
	if next.Status != pb.GameStatus_GAME_STATUS_PAUSED {
		t.Errorf("Status: got %v, want PAUSED", next.Status)
	}
}

// ── player lifecycle ──────────────────────────────────────────────────────────

func TestApply_PlayerJoined(t *testing.T) {
	state := &pb.GameState{
		GameId:  "g",
		Players: map[string]*pb.PlayerState{},
	}
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerJoined{PlayerJoined: &pb.PlayerJoined{
		PlayerId: "p3",
		UserId:   "u3",
		Seat:     2,
		BuyIn:    500,
	}}})
	p := next.Players["p3"]
	if p == nil {
		t.Fatal("player p3 not added")
	}
	if p.Stack != 500 {
		t.Errorf("Stack: got %d, want 500", p.Stack)
	}
	if p.Seat != 2 {
		t.Errorf("Seat: got %d, want 2", p.Seat)
	}
}

func TestApply_PlayerLeft(t *testing.T) {
	state := baseState()
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerLeft{PlayerLeft: &pb.PlayerLeft{PlayerId: "p1"}}})
	if _, ok := next.Players["p1"]; ok {
		t.Error("p1 still in Players after PlayerLeft")
	}
}

func TestApply_PlayerSatOut(t *testing.T) {
	state := baseState()
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerSatOut{PlayerSatOut: &pb.PlayerSatOut{PlayerId: "p1"}}})
	if !next.Players["p1"].IsSittingOut {
		t.Error("IsSittingOut not set after PlayerSatOut")
	}
	if next.Players["p1"].SeatStatus != pb.SeatStatus_SEAT_STATUS_SITTING_OUT {
		t.Errorf("SeatStatus: got %v, want SITTING_OUT", next.Players["p1"].SeatStatus)
	}
}

// ── hand lifecycle ────────────────────────────────────────────────────────────

func TestApply_HandStarted_InitializesHand(t *testing.T) {
	state := baseState()
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_HandStarted{HandStarted: &pb.HandStarted{
		HandId:         "h1",
		HandNumber:     1,
		DealerPlayerId: "p1",
		SbPlayerId:     "p1",
		BbPlayerId:     "p2",
		SmallBlind:     50,
		BigBlind:       100,
	}}})

	if next.CurrentHand == nil {
		t.Fatal("CurrentHand is nil after HandStarted")
	}
	if next.CurrentHand.HandId != "h1" {
		t.Errorf("HandId: got %q, want h1", next.CurrentHand.HandId)
	}
	if next.Status != pb.GameStatus_GAME_STATUS_ACTIVE {
		t.Errorf("Status: got %v, want ACTIVE", next.Status)
	}
	if len(next.CurrentHand.ActionOrder) == 0 {
		t.Error("ActionOrder is empty after HandStarted")
	}
	if next.DealerSeat != next.Players["p1"].Seat {
		t.Errorf("DealerSeat: got %d, want %d", next.DealerSeat, next.Players["p1"].Seat)
	}
}

func TestApply_HandStarted_ResetsFolded(t *testing.T) {
	state := baseState()
	state.Players["p1"].IsFolded = true
	state.Players["p2"].IsAllIn = true

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_HandStarted{HandStarted: &pb.HandStarted{
		HandId:         "h2",
		DealerPlayerId: "p1",
		SbPlayerId:     "p1",
		BbPlayerId:     "p2",
	}}})

	if next.Players["p1"].IsFolded {
		t.Error("IsFolded not cleared on p1")
	}
	if next.Players["p2"].IsAllIn {
		t.Error("IsAllIn not cleared on p2")
	}
}

func TestApply_BlindPosted_UpdatesPotAndStack(t *testing.T) {
	state := baseState()
	state.CurrentHand = &pb.HandState{
		Phase:      pb.HandPhase_HAND_PHASE_PREFLOP,
		ActionOrder: []string{"p1", "p2"},
	}
	state.Players["p2"].Stack = 1000

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_BlindPosted{BlindPosted: &pb.BlindPosted{
		PlayerId:     "p2",
		Amount:       50,
		IsSmallBlind: true,
	}}})

	if next.Players["p2"].Stack != 950 {
		t.Errorf("Stack after SB: got %d, want 950", next.Players["p2"].Stack)
	}
	if next.CurrentHand.Pot != 50 {
		t.Errorf("Pot after SB: got %d, want 50", next.CurrentHand.Pot)
	}
	if next.CurrentHand.CurrentHighestBet != 50 {
		t.Errorf("CurrentHighestBet: got %d, want 50", next.CurrentHand.CurrentHighestBet)
	}
}

// ── player actions ────────────────────────────────────────────────────────────

func TestApply_PlayerFolded_MarksFolded(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerFolded{PlayerFolded: &pb.PlayerFolded{PlayerId: "p1"}}})

	if !next.Players["p1"].IsFolded {
		t.Error("IsFolded not set after PlayerFolded")
	}
	if next.CurrentHand.ActingPlayerId != nil {
		t.Error("ActingPlayerId should be nil after fold")
	}
	for _, pid := range next.CurrentHand.PendingActions {
		if pid == "p1" {
			t.Error("p1 still in PendingActions after fold")
		}
	}
}

func TestApply_PlayerChecked_RemovesFromPending(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerChecked{PlayerChecked: &pb.PlayerChecked{PlayerId: "p1"}}})

	if next.CurrentHand.ActingPlayerId != nil {
		t.Error("ActingPlayerId should be nil after check")
	}
	for _, pid := range next.CurrentHand.PendingActions {
		if pid == "p1" {
			t.Error("p1 still in PendingActions after check")
		}
	}
}

func TestApply_PlayerBet_ResetsOthersPending(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")
	state.CurrentHand.CurrentHighestBet = 0
	state.CurrentHand.PendingActions = []string{"p1"}
	state.CurrentHand.ActingPlayerId = ptr("p1")

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerBet{PlayerBet: &pb.PlayerBet{
		PlayerId: "p1",
		Amount:   200,
	}}})

	if next.CurrentHand.CurrentHighestBet != 200 {
		t.Errorf("CurrentHighestBet: got %d, want 200", next.CurrentHand.CurrentHighestBet)
	}
	if next.Players["p1"].CurrentBet != 200 {
		t.Errorf("p1.CurrentBet: got %d, want 200", next.Players["p1"].CurrentBet)
	}
	// p2 (the non-bettor) should now be pending.
	found := false
	for _, pid := range next.CurrentHand.PendingActions {
		if pid == "p2" {
			found = true
		}
	}
	if !found {
		t.Error("p2 not in PendingActions after p1 bet")
	}
}

func TestApply_PlayerCalled_UpdatesStack(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")
	state.CurrentHand.CurrentHighestBet = 100
	state.Players["p1"].CurrentBet = 0
	state.Players["p1"].Stack = 1000

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerCalled{PlayerCalled: &pb.PlayerCalled{
		PlayerId: "p1",
		Amount:   100,
	}}})

	if next.Players["p1"].Stack != 900 {
		t.Errorf("Stack after call: got %d, want 900", next.Players["p1"].Stack)
	}
	if next.CurrentHand.Pot != 150+100 {
		t.Errorf("Pot: got %d, want 250", next.CurrentHand.Pot)
	}
}

func TestApply_PlayerAllIn_SetsAllIn(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")
	state.Players["p1"].Stack = 200
	state.Players["p1"].CurrentBet = 0
	state.CurrentHand.CurrentHighestBet = 100

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerAllIn{PlayerAllIn: &pb.PlayerAllIn{
		PlayerId: "p1",
		Amount:   200, // total bet after going all-in
	}}})

	if !next.Players["p1"].IsAllIn {
		t.Error("IsAllIn not set after PlayerAllIn")
	}
	if next.Players["p1"].Stack != 0 {
		t.Errorf("Stack after all-in: got %d, want 0", next.Players["p1"].Stack)
	}
}

func TestApply_PlayerAllIn_SubMinRaise_ClosesAction(t *testing.T) {
	// Scenario: P2 raised to 200 (BB=100, LastRaiseAmount=100).
	// P1 had already acted (matched 200). P3 goes all-in for 260 (raiseBy=60 < 100).
	// → P1's raise option should be closed; P2's should not (P2 was pending).
	state := &pb.GameState{
		GameId: "g1",
		Players: map[string]*pb.PlayerState{
			"p1": {PlayerId: "p1", Seat: 0, Stack: 800, CurrentBet: 200, IsAllIn: false},
			"p2": {PlayerId: "p2", Seat: 1, Stack: 500, CurrentBet: 200, IsAllIn: false},
			"p3": {PlayerId: "p3", Seat: 2, Stack: 260, CurrentBet: 200, IsAllIn: false},
		},
		CurrentHand: &pb.HandState{
			Phase:             pb.HandPhase_HAND_PHASE_PREFLOP,
			CurrentHighestBet: 200,
			LastRaiseAmount:   100, // P2's prior raise was 100
			ActionOrder:       []string{"p1", "p2", "p3"},
			PendingActions:    []string{"p2", "p3"}, // p1 already called; p2 and p3 still pending
		},
	}

	// P3 goes all-in for 260 (total), raiseBy=60 < 100 → sub-min raise.
	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerAllIn{PlayerAllIn: &pb.PlayerAllIn{
		PlayerId: "p3",
		Amount:   260,
	}}})

	// P3 is all-in.
	if !next.Players["p3"].IsAllIn {
		t.Error("expected p3 IsAllIn=true")
	}
	// New highest bet is 260.
	if next.CurrentHand.CurrentHighestBet != 260 {
		t.Errorf("CurrentHighestBet: got %d, want 260", next.CurrentHand.CurrentHighestBet)
	}
	// P1 was not pending → action closed for p1.
	closedSet := make(map[string]bool)
	for _, pid := range next.CurrentHand.ActionClosedFor {
		closedSet[pid] = true
	}
	if !closedSet["p1"] {
		t.Error("expected p1 raise action to be closed (already acted, sub-min raise)")
	}
	if closedSet["p2"] {
		t.Error("p2 was pending before all-in — raise action must NOT be closed")
	}
	// Both p1 and p2 are in PendingActions (need to call 60 more).
	pendingSet := make(map[string]bool)
	for _, pid := range next.CurrentHand.PendingActions {
		pendingSet[pid] = true
	}
	if !pendingSet["p1"] {
		t.Error("p1 must be in PendingActions to call the extra 60")
	}
	if !pendingSet["p2"] {
		t.Error("p2 must be in PendingActions")
	}
}

func TestApply_PlayerAllIn_FullRaise_ReopensAction(t *testing.T) {
	// P2 raised to 200. P1 already called. P3 goes all-in for 350 (raiseBy=150 >= 100).
	// → Full raise: action_closed_for must be nil; everyone is re-opened.
	state := &pb.GameState{
		GameId: "g1",
		Players: map[string]*pb.PlayerState{
			"p1": {PlayerId: "p1", Seat: 0, Stack: 1000, CurrentBet: 200},
			"p2": {PlayerId: "p2", Seat: 1, Stack: 800, CurrentBet: 200},
			"p3": {PlayerId: "p3", Seat: 2, Stack: 350, CurrentBet: 200},
		},
		CurrentHand: &pb.HandState{
			Phase:             pb.HandPhase_HAND_PHASE_PREFLOP,
			CurrentHighestBet: 200,
			LastRaiseAmount:   100,
			ActionOrder:       []string{"p1", "p2", "p3"},
			PendingActions:    []string{"p2", "p3"},
		},
	}

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PlayerAllIn{PlayerAllIn: &pb.PlayerAllIn{
		PlayerId: "p3",
		Amount:   350,
	}}})

	if len(next.CurrentHand.ActionClosedFor) != 0 {
		t.Errorf("expected ActionClosedFor empty after full raise, got %v", next.CurrentHand.ActionClosedFor)
	}
	if next.CurrentHand.LastFullRaiseTo != 350 {
		t.Errorf("LastFullRaiseTo: got %d, want 350", next.CurrentHand.LastFullRaiseTo)
	}
}

// ── board cards ───────────────────────────────────────────────────────────────

func TestApply_FlopDealt_SetsPhaseAndCards(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_FlopDealt{FlopDealt: &pb.FlopDealt{
		Card_1: 10,
		Card_2: 20,
		Card_3: 30,
	}}})

	if next.CurrentHand.Phase != pb.HandPhase_HAND_PHASE_FLOP {
		t.Errorf("Phase: got %v, want FLOP", next.CurrentHand.Phase)
	}
	if next.CurrentHand.FlopCard_1 == nil || *next.CurrentHand.FlopCard_1 != 10 {
		t.Error("FlopCard_1 not set correctly")
	}
}

func TestApply_TurnDealt_SetsPhase(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_TurnDealt{TurnDealt: &pb.TurnDealt{Card: 40}}})

	if next.CurrentHand.Phase != pb.HandPhase_HAND_PHASE_TURN {
		t.Errorf("Phase: got %v, want TURN", next.CurrentHand.Phase)
	}
	if next.CurrentHand.TurnCard == nil || *next.CurrentHand.TurnCard != 40 {
		t.Error("TurnCard not set correctly")
	}
}

// ── pot / showdown ────────────────────────────────────────────────────────────

func TestApply_BettingRoundEnded_ClearsBets(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")
	state.CurrentHand.RaisesThisStreet = 2
	state.Players["p1"].CurrentBet = 200
	state.Players["p2"].CurrentBet = 200

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_BettingRoundEnded{BettingRoundEnded: &pb.BettingRoundEnded{}}})

	if next.CurrentHand.CurrentHighestBet != 0 {
		t.Errorf("CurrentHighestBet: got %d, want 0", next.CurrentHand.CurrentHighestBet)
	}
	if next.CurrentHand.RaisesThisStreet != 0 {
		t.Errorf("RaisesThisStreet: got %d, want 0", next.CurrentHand.RaisesThisStreet)
	}
	if next.Players["p1"].CurrentBet != 0 || next.Players["p2"].CurrentBet != 0 {
		t.Error("CurrentBet not cleared after BettingRoundEnded")
	}
}

func TestApply_PotAwarded_IncrementsWinnerStack(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")
	state.CurrentHand.Pot = 500
	state.Players["p2"].Stack = 800

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_PotAwarded{PotAwarded: &pb.PotAwarded{
		Amount:          500,
		WinnerPlayerIds: []string{"p2"},
	}}})

	if next.Players["p2"].Stack != 1300 {
		t.Errorf("Stack after PotAwarded: got %d, want 1300", next.Players["p2"].Stack)
	}
	if next.CurrentHand.Pot != 0 {
		t.Errorf("Pot after PotAwarded: got %d, want 0", next.CurrentHand.Pot)
	}
}

func TestApply_HandEnded_ClearsCurrentHand(t *testing.T) {
	state := baseState()
	state.CurrentHand = handState("p2", "p2", "p1")
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE

	next := mustApply(t, state, &pb.GameEvent{Event: &pb.GameEvent_HandEnded{HandEnded: &pb.HandEnded{HandNumber: 1}}})

	if next.CurrentHand != nil {
		t.Error("CurrentHand not cleared after HandEnded")
	}
	if next.HandNumber != 1 {
		t.Errorf("HandNumber: got %d, want 1", next.HandNumber)
	}
	if next.Status != pb.GameStatus_GAME_STATUS_WAITING {
		t.Errorf("Status: got %v, want WAITING", next.Status)
	}
}

// ── version increment ─────────────────────────────────────────────────────────

func TestApply_VersionIncrements(t *testing.T) {
	state := baseState()
	state.Version = 5

	next := apply(state, &pb.GameEvent{Event: &pb.GameEvent_TableClosed{TableClosed: &pb.TableClosed{}}})
	if next.Version != 6 {
		t.Errorf("Version: got %d, want 6", next.Version)
	}
}

func TestApply_StateVersionOverridesIncrement(t *testing.T) {
	state := baseState()
	state.Version = 5

	next := apply(state, &pb.GameEvent{
		StateVersion: 100,
		Event:        &pb.GameEvent_TableClosed{TableClosed: &pb.TableClosed{}},
	})
	if next.Version != 100 {
		t.Errorf("Version: got %d, want 100", next.Version)
	}
}

// ── applyAll ──────────────────────────────────────────────────────────────────

func TestApplyAll_AppliesSequentially(t *testing.T) {
	state := baseState()
	events := []*pb.GameEvent{
		{Event: &pb.GameEvent_TableClosed{TableClosed: &pb.TableClosed{}}},
	}
	next := applyAll(state, events)
	if next.Status != pb.GameStatus_GAME_STATUS_CLOSED {
		t.Errorf("Status after applyAll: got %v, want CLOSED", next.Status)
	}
}

func TestApplyAll_EmptySlice(t *testing.T) {
	state := baseState()
	next := applyAll(state, nil)
	if next != state {
		t.Error("applyAll(state, nil) should return the same pointer")
	}
}