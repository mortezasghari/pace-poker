package engine

import (
	"testing"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/deck"
)

// ── fixtures ──────────────────────────────────────────────────────────────────

func activeHandState() *pb.GameState {
	state := &pb.GameState{
		GameId: "game-1",
		Status: pb.GameStatus_GAME_STATUS_ACTIVE,
		Config: &pb.CashGameConfig{
			SmallBlind: 50,
			BigBlind:   100,
		},
		Players: map[string]*pb.PlayerState{
			"p1": {PlayerId: "p1", Seat: 0, Stack: 900, CurrentBet: 100},
			"p2": {PlayerId: "p2", Seat: 1, Stack: 950, CurrentBet: 50},
		},
	}
	actingID := "p1"
	state.CurrentHand = &pb.HandState{
		HandId:             "h1",
		DealerPlayerId:     "p2",
		SmallBlindPlayerId: "p2",
		BigBlindPlayerId:   "p1",
		Phase:              pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:                150,
		CurrentHighestBet:  100,
		MinRaiseAmount:     100,
		LastRaiseAmount:    100,
		ActionOrder:        []string{"p1", "p2"},
		PendingActions:     []string{"p1"},
		ActingPlayerId:     &actingID,
	}
	return state
}

func foldCmd() *pb.PlayerCommand {
	return &pb.PlayerCommand{CommandId: uuid.New().String(), GameId: "game-1",
		Payload: &pb.PlayerCommand_Fold{Fold: &pb.FoldCommand{}}}
}
func checkCmd() *pb.PlayerCommand {
	return &pb.PlayerCommand{CommandId: uuid.New().String(), GameId: "game-1",
		Payload: &pb.PlayerCommand_Check{Check: &pb.CheckCommand{}}}
}
func callCmd() *pb.PlayerCommand {
	return &pb.PlayerCommand{CommandId: uuid.New().String(), GameId: "game-1",
		Payload: &pb.PlayerCommand_Call{Call: &pb.CallCommand{}}}
}
func allInCmd() *pb.PlayerCommand {
	return &pb.PlayerCommand{CommandId: uuid.New().String(), GameId: "game-1",
		Payload: &pb.PlayerCommand_AllIn{AllIn: &pb.AllInCommand{}}}
}
func chatCmd(text string) *pb.PlayerCommand {
	return &pb.PlayerCommand{CommandId: uuid.New().String(), GameId: "game-1",
		Payload: &pb.PlayerCommand_ChatMessage{ChatMessage: &pb.ChatMessageCommand{Text: text}}}
}

// ── validation: rejected commands ────────────────────────────────────────────

func TestHandleCommand_NoHand_FoldRejected(t *testing.T) {
	state := twoPlayerState() // WAITING, no hand
	dlr := NewFixedDealer(fullDeck())
	cmd := foldCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected events, got none")
	}
	if _, ok := events[0].Event.(*pb.GameEvent_CommandRejected); !ok {
		t.Errorf("first event: got %T, want CommandRejected", events[0].Event)
	}
}

func TestHandleCommand_WrongTurn_Rejected(t *testing.T) {
	state := activeHandState()
	dlr := NewFixedDealer(fullDeck())
	// p2's turn is not set — p1 is acting, not p2.
	cmd := foldCmd()

	events, err := HandleCommand(state, cmd, "p2", dlr)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := events[0].Event.(*pb.GameEvent_CommandRejected); !ok {
		t.Errorf("expected CommandRejected for wrong-turn fold, got %T", events[0].Event)
	}
}

// ── valid actions ─────────────────────────────────────────────────────────────

func TestHandleCommand_Fold_ProducesFoldedEvent(t *testing.T) {
	state := activeHandState()
	dlr := NewFixedDealer(fullDeck())
	cmd := foldCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}

	var foldFound bool
	for _, e := range events {
		if f, ok := e.Event.(*pb.GameEvent_PlayerFolded); ok {
			if f.PlayerFolded.GetPlayerId() == "p1" {
				foldFound = true
			}
		}
	}
	if !foldFound {
		t.Error("expected PlayerFolded event for p1")
	}
}

func TestHandleCommand_Fold_TriggersHandEnd(t *testing.T) {
	// p1 folds → p2 is last player → pot is awarded → hand ends.
	state := activeHandState()
	dlr := NewFixedDealer(fullDeck())
	cmd := foldCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}

	var handEnded bool
	var potAwarded bool
	for _, e := range events {
		if _, ok := e.Event.(*pb.GameEvent_HandEnded); ok {
			handEnded = true
		}
		if _, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			potAwarded = true
		}
	}
	if !potAwarded {
		t.Error("expected PotAwarded after last player folds")
	}
	if !handEnded {
		t.Error("expected HandEnded after last player folds")
	}
}

func TestHandleCommand_Check_ProducesCheckEvent(t *testing.T) {
	state := activeHandState()
	// Set up a state where check is valid: no bet outstanding, actor's turn.
	state.CurrentHand.CurrentHighestBet = 0
	state.Players["p1"].CurrentBet = 0
	dlr := NewFixedDealer(fullDeck())
	cmd := checkCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, e := range events {
		if _, ok := e.Event.(*pb.GameEvent_PlayerChecked); ok {
			found = true
		}
	}
	if !found {
		t.Error("expected PlayerChecked event")
	}
}

func TestHandleCommand_Call_ProducesCallEvent(t *testing.T) {
	state := activeHandState()
	// p1 (BB) has CurrentBet=100, CurrentHighestBet=100 — nothing to call.
	// Let's set UTG scenario: p1 has bet 0, needs to call 100.
	state.Players["p1"].CurrentBet = 0
	state.Players["p1"].Stack = 1000
	state.CurrentHand.CurrentHighestBet = 100
	dlr := NewFixedDealer(fullDeck())
	cmd := callCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatal(err)
	}

	var called *pb.PlayerCalled
	for _, e := range events {
		if c, ok := e.Event.(*pb.GameEvent_PlayerCalled); ok {
			called = c.PlayerCalled
		}
	}
	if called == nil {
		t.Fatal("expected PlayerCalled event")
	}
	if called.Amount != 100 {
		t.Errorf("Call amount: got %d, want 100", called.Amount)
	}
}

func TestHandleCommand_Call_CappedAtStack(t *testing.T) {
	state := activeHandState()
	// p1 has only 60 chips, needs to call 100 → call is capped at 60.
	state.Players["p1"].Stack = 60
	state.Players["p1"].CurrentBet = 0
	state.CurrentHand.CurrentHighestBet = 100
	dlr := NewFixedDealer(fullDeck())
	cmd := callCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatal(err)
	}

	var called *pb.PlayerCalled
	for _, e := range events {
		if c, ok := e.Event.(*pb.GameEvent_PlayerCalled); ok {
			called = c.PlayerCalled
		}
	}
	if called == nil {
		t.Fatal("expected PlayerCalled event")
	}
	if called.Amount != 60 {
		t.Errorf("capped call amount: got %d, want 60", called.Amount)
	}
	// After applying, player should be all-in.
	finalState := applyAll(state, events)
	if !finalState.Players["p1"].IsAllIn {
		t.Error("expected player to be all-in after calling with full stack")
	}
}

func TestHandleCommand_AllIn_SetsAllIn(t *testing.T) {
	state := activeHandState()
	state.Players["p1"].Stack = 500
	state.Players["p1"].CurrentBet = 0
	dlr := NewFixedDealer(fullDeck())
	cmd := allInCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatal(err)
	}

	var found *pb.PlayerAllIn
	for _, e := range events {
		if a, ok := e.Event.(*pb.GameEvent_PlayerAllIn); ok {
			found = a.PlayerAllIn
		}
	}
	if found == nil {
		t.Fatal("expected PlayerAllIn event")
	}
	if found.Amount != 500 {
		t.Errorf("AllIn amount: got %d, want 500", found.Amount)
	}
}

// ── chat / emote (non-action commands) ───────────────────────────────────────

func TestHandleCommand_Chat_EmitsChatEvent(t *testing.T) {
	state := activeHandState()
	state.Config.AllowChat = true
	dlr := NewFixedDealer(fullDeck())
	cmd := chatCmd("hello")

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatal(err)
	}

	var found *pb.ChatMessageSent
	for _, e := range events {
		if c, ok := e.Event.(*pb.GameEvent_ChatMessageSent); ok {
			found = c.ChatMessageSent
		}
	}
	if found == nil {
		t.Fatal("expected ChatMessageSent event")
	}
	if found.Text != "hello" {
		t.Errorf("Text: got %q, want %q", found.Text, "hello")
	}
}

// ── advance after command ─────────────────────────────────────────────────────

func TestHandleCommand_StartsNextActionAfterCheck(t *testing.T) {
	// p1 checks in a two-player hand where both must check (no bet).
	// After p1 checks, p2 should get ActionStarted.
	state := activeHandState()
	state.CurrentHand.CurrentHighestBet = 0
	state.Players["p1"].CurrentBet = 0
	state.Players["p2"].CurrentBet = 0
	state.CurrentHand.PendingActions = []string{"p1", "p2"}
	dlr := NewFixedDealer(fullDeck())
	cmd := checkCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatal(err)
	}

	var actionStarted *pb.ActionStarted
	for _, e := range events {
		if a, ok := e.Event.(*pb.GameEvent_ActionStarted); ok {
			actionStarted = a.ActionStarted
		}
	}
	if actionStarted == nil {
		t.Fatal("expected ActionStarted for p2 after p1 checks")
	}
	if actionStarted.PlayerId != "p2" {
		t.Errorf("ActionStarted.PlayerId: got %q, want p2", actionStarted.PlayerId)
	}
}

// ── new hand starts after old one ends ───────────────────────────────────────

func TestHandleCommand_FoldEndsHandAndStartsNext(t *testing.T) {
	state := activeHandState()
	// p1 and p2 are the only players; ensure enough stacks for next hand.
	state.Players["p1"].Stack = 1000
	state.Players["p2"].Stack = 1000

	// Use a full deck (many cards available for next hand too).
	cards := make([]deck.Card, deck.Size*4)
	for i := range cards {
		cards[i] = deck.Card(i % deck.Size)
	}
	dlr := NewFixedDealer(cards)

	cmd := foldCmd()

	events, err := HandleCommand(state, cmd, "p1", dlr)
	if err != nil {
		t.Fatalf("HandleCommand: %v", err)
	}

	// Should have: PlayerFolded, PotAwarded, HandEnded, (new hand) HandStarted, ...
	var handStartedCount int
	for _, e := range events {
		if _, ok := e.Event.(*pb.GameEvent_HandStarted); ok {
			handStartedCount++
		}
	}
	if handStartedCount == 0 {
		t.Error("expected new HandStarted after hand ends")
	}
}