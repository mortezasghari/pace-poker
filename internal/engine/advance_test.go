package engine

import (
	"testing"
	"time"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/deck"
)

// ── fixtures ──────────────────────────────────────────────────────────────────

func twoPlayerState() *pb.GameState {
	return &pb.GameState{
		GameId: "g1",
		Status: pb.GameStatus_GAME_STATUS_WAITING,
		Config: &pb.CashGameConfig{
			SmallBlind:        50,
			BigBlind:          100,
			MinPlayersToStart: 2,
		},
		Players: map[string]*pb.PlayerState{
			"p1": {PlayerId: "p1", Seat: 0, Stack: 1000},
			"p2": {PlayerId: "p2", Seat: 1, Stack: 1000},
		},
	}
}

func fullDeck() []deck.Card {
	cards := make([]deck.Card, deck.Size)
	for i := range cards {
		cards[i] = deck.Card(i)
	}
	return cards
}

func fixedDealer() Dealer { return NewFixedDealer(fullDeck()) }

// advanceUntilQuiescent runs advance in a loop until it returns nil, returning
// all emitted events and the final state.
func advanceUntilQuiescent(state *pb.GameState, dlr Dealer) (*pb.GameState, []*pb.GameEvent, error) {
	var all []*pb.GameEvent
	now := time.Time{}
	for range 64 {
		batch, err := advance(state, dlr, now)
		if err != nil {
			return state, all, err
		}
		if len(batch) == 0 {
			break
		}
		state = applyAll(state, batch)
		all = append(all, batch...)
	}
	return state, all, nil
}

// ── tests: no-hand (idle) ─────────────────────────────────────────────────────

func TestAdvance_NoHand_NotEnoughPlayers_ReturnsNil(t *testing.T) {
	state := twoPlayerState()
	// Remove one player so we're below minimum.
	delete(state.Players, "p2")

	batch, err := advance(state, fixedDealer(), time.Time{})
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(batch) != 0 {
		t.Errorf("expected no events with 1 player, got %d events", len(batch))
	}
}

func TestAdvance_NoHand_GamePaused_ReturnsNil(t *testing.T) {
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_PAUSED

	batch, err := advance(state, fixedDealer(), time.Time{})
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(batch) != 0 {
		t.Errorf("expected no events when paused, got %d", len(batch))
	}
}

func TestAdvance_NoHand_StartsHandWithEnoughPlayers(t *testing.T) {
	state := twoPlayerState()

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected hand-start events, got none")
	}

	// Verify HandStarted is the first event.
	if _, ok := events[0].Event.(*pb.GameEvent_HandStarted); !ok {
		t.Errorf("first event: got %T, want HandStarted", events[0].Event)
	}
}

func TestAdvance_NewHand_EmitsBlindPosted(t *testing.T) {
	state := twoPlayerState()

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var blinds []*pb.BlindPosted
	for _, e := range events {
		if b, ok := e.Event.(*pb.GameEvent_BlindPosted); ok {
			blinds = append(blinds, b.BlindPosted)
		}
	}
	if len(blinds) != 2 {
		t.Errorf("expected 2 BlindPosted events, got %d", len(blinds))
	}
}

func TestAdvance_NewHand_EmitsHoleCards(t *testing.T) {
	state := twoPlayerState()

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var revealed []*pb.HoleCardsRevealed
	for _, e := range events {
		if r, ok := e.Event.(*pb.GameEvent_HoleCardsRevealed); ok {
			revealed = append(revealed, r.HoleCardsRevealed)
		}
	}
	// 2 players × 1 HoleCardsRevealed each.
	if len(revealed) != 2 {
		t.Errorf("expected 2 HoleCardsRevealed events, got %d", len(revealed))
	}
}

func TestAdvance_NewHand_EndsWithActionStarted(t *testing.T) {
	state := twoPlayerState()

	finalState, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	last := events[len(events)-1]
	if _, ok := last.Event.(*pb.GameEvent_ActionStarted); !ok {
		t.Errorf("last event: got %T, want ActionStarted", last.Event)
	}
	// The final state must have an ActingPlayerId set.
	if finalState.CurrentHand == nil || finalState.CurrentHand.ActingPlayerId == nil {
		t.Error("expected ActingPlayerId set after advance settles")
	}
}

func TestAdvance_WaitingForPlayer_ReturnsNil(t *testing.T) {
	state := twoPlayerState()

	// Advance once to start the hand.
	finalState, _, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	// Advance again — should return nil because we're waiting for the actor.
	batch, err := advance(finalState, fixedDealer(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Errorf("expected nil batch while waiting for player, got %d events", len(batch))
	}
}

// ── tests: uncontested ────────────────────────────────────────────────────────

func TestAdvance_Uncontested_AwardsPotAndEndsHand(t *testing.T) {
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	state.CurrentHand = &pb.HandState{
		Phase:       pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:         300,
		ActionOrder: []string{"p1", "p2"},
	}
	state.Players["p1"].IsFolded = true
	state.Players["p2"].IsFolded = false

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var potAwarded *pb.PotAwarded
	var handEnded *pb.HandEnded
	for _, e := range events {
		if p, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			potAwarded = p.PotAwarded
		}
		if h, ok := e.Event.(*pb.GameEvent_HandEnded); ok {
			handEnded = h.HandEnded
		}
	}
	if potAwarded == nil {
		t.Fatal("expected PotAwarded event")
	}
	if potAwarded.Amount != 300 {
		t.Errorf("PotAwarded.Amount: got %d, want 300", potAwarded.Amount)
	}
	if len(potAwarded.WinnerPlayerIds) != 1 || potAwarded.WinnerPlayerIds[0] != "p2" {
		t.Errorf("winner: got %v, want [p2]", potAwarded.WinnerPlayerIds)
	}
	if handEnded == nil {
		t.Fatal("expected HandEnded event")
	}
}

// ── tests: street advancement ─────────────────────────────────────────────────

func TestAdvance_PreflopDone_DealsFlop(t *testing.T) {
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:            200,
		ActionOrder:    []string{"p1", "p2"},
		PendingActions: nil, // betting round complete
		ActingPlayerId: nil,
	}

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var bre *pb.BettingRoundEnded
	var flop *pb.FlopDealt
	for _, e := range events {
		if b, ok := e.Event.(*pb.GameEvent_BettingRoundEnded); ok {
			bre = b.BettingRoundEnded
		}
		if f, ok := e.Event.(*pb.GameEvent_FlopDealt); ok {
			flop = f.FlopDealt
		}
	}
	if bre == nil {
		t.Fatal("expected BettingRoundEnded")
	}
	if flop == nil {
		t.Fatal("expected FlopDealt")
	}
	// Flop cards must be distinct.
	if flop.Card_1 == flop.Card_2 || flop.Card_1 == flop.Card_3 || flop.Card_2 == flop.Card_3 {
		t.Errorf("duplicate flop cards: %d %d %d", flop.Card_1, flop.Card_2, flop.Card_3)
	}
}

func TestAdvance_FlopDone_DealsTurn(t *testing.T) {
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	c1, c2, c3 := uint32(0), uint32(1), uint32(2)
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_FLOP,
		Pot:            200,
		ActionOrder:    []string{"p1", "p2"},
		PendingActions: nil,
		ActingPlayerId: nil,
		FlopCard_1:     &c1,
		FlopCard_2:     &c2,
		FlopCard_3:     &c3,
	}

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var turn *pb.TurnDealt
	for _, e := range events {
		if tu, ok := e.Event.(*pb.GameEvent_TurnDealt); ok {
			turn = tu.TurnDealt
		}
	}
	if turn == nil {
		t.Fatal("expected TurnDealt after flop done")
	}
}

func TestAdvance_RiverDone_RunsShowdown(t *testing.T) {
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	c := uint32(1)
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_RIVER,
		Pot:            400,
		ActionOrder:    []string{"p1", "p2"},
		PendingActions: nil,
		ActingPlayerId: nil,
		RiverCard:      &c,
	}

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var showdown *pb.Showdown
	var potAwarded *pb.PotAwarded
	var handEnded *pb.HandEnded
	for _, e := range events {
		if s, ok := e.Event.(*pb.GameEvent_Showdown); ok {
			showdown = s.Showdown
		}
		if p, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			potAwarded = p.PotAwarded
		}
		if h, ok := e.Event.(*pb.GameEvent_HandEnded); ok {
			handEnded = h.HandEnded
		}
	}
	if showdown == nil {
		t.Fatal("expected Showdown event after river done")
	}
	if potAwarded == nil {
		t.Fatal("expected PotAwarded event")
	}
	if potAwarded.Amount != 400 {
		t.Errorf("PotAwarded.Amount: got %d, want 400", potAwarded.Amount)
	}
	if handEnded == nil {
		t.Fatal("expected HandEnded event")
	}
}

// ── tests: heads-up blind and action order ───────────────────────────────────

func TestAdvance_HU_DealerIsSmallBlind(t *testing.T) {
	// In heads-up, dealer = SB. Verify via BlindPosted events.
	state := twoPlayerState()

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	// Find HandStarted to learn who is dealer.
	var hs *pb.HandStarted
	for _, e := range events {
		if h, ok := e.Event.(*pb.GameEvent_HandStarted); ok {
			hs = h.HandStarted
		}
	}
	if hs == nil {
		t.Fatal("expected HandStarted")
	}
	// In heads-up: SB == Dealer.
	if hs.SbPlayerId != hs.DealerPlayerId {
		t.Errorf("HU: SB (%s) != Dealer (%s); dealer must be SB in heads-up",
			hs.SbPlayerId, hs.DealerPlayerId)
	}
}

func TestAdvance_HU_Preflop_DealerActsFirst(t *testing.T) {
	// In heads-up preflop, the dealer (SB) acts first.
	state := twoPlayerState()

	finalState, _, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	if finalState.CurrentHand == nil || finalState.CurrentHand.ActingPlayerId == nil {
		t.Fatal("expected an acting player after setup")
	}
	actingID := *finalState.CurrentHand.ActingPlayerId
	dealerID := finalState.CurrentHand.DealerPlayerId
	if actingID != dealerID {
		t.Errorf("HU preflop: acting=%s dealer=%s; dealer (SB) must act first preflop",
			actingID, dealerID)
	}
}

func TestAdvance_HU_Postflop_NonDealerActsFirst(t *testing.T) {
	// In heads-up postflop, the non-dealer (BB) acts first.
	state := twoPlayerState()

	afterSetup, _, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}
	dealerID := afterSetup.CurrentHand.DealerPlayerId

	// Skip preflop: clear pending actions and advance to flop.
	afterSetup.CurrentHand.PendingActions = nil
	afterSetup.CurrentHand.ActingPlayerId = nil
	afterSetup.CurrentHand.CurrentHighestBet = 0
	for _, p := range afterSetup.Players {
		p.CurrentBet = 0
	}

	afterFlop, _, err := advanceUntilQuiescent(afterSetup, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	if afterFlop.CurrentHand == nil || afterFlop.CurrentHand.ActingPlayerId == nil {
		t.Fatal("expected acting player after flop dealt")
	}
	actingID := *afterFlop.CurrentHand.ActingPlayerId
	if actingID == dealerID {
		t.Errorf("HU postflop: acting=%s dealer=%s; non-dealer must act first postflop",
			actingID, dealerID)
	}
}

// ── tests: dealer button rotation ─────────────────────────────────────────────

// ── tests: zero-stack auto-sat-out ───────────────────────────────────────────

func TestAdvance_BustedPlayer_AutoSatOut(t *testing.T) {
	// p1 has stack=0 (busted) but is not sitting out → advance should sat them out.
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_WAITING
	state.Players["p1"].Stack = 0

	batch, err := advance(state, fixedDealer(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var satOut *pb.PlayerSatOut
	for _, e := range batch {
		if s, ok := e.Event.(*pb.GameEvent_PlayerSatOut); ok {
			satOut = s.PlayerSatOut
		}
	}
	if satOut == nil {
		t.Fatal("expected PlayerSatOut for busted player")
	}
	if satOut.PlayerId != "p1" {
		t.Errorf("sat-out player: got %q, want p1", satOut.PlayerId)
	}
}

func TestAdvance_BustedPlayer_ExcludedFromNewHand(t *testing.T) {
	// After busted player is sat out, advance should not deal them into the next hand.
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_WAITING
	state.Players["p1"].Stack = 0

	finalState, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var handStarted *pb.HandStarted
	for _, e := range events {
		if h, ok := e.Event.(*pb.GameEvent_HandStarted); ok {
			handStarted = h.HandStarted
		}
	}

	// p1 is busted — only p2 remains (below min players), so no hand should start.
	if handStarted != nil {
		t.Error("expected no hand to start with only one funded player")
	}
	if !finalState.Players["p1"].IsSittingOut {
		t.Error("expected p1 to be sitting out after bust")
	}
}

func TestAdvance_DealerRotates_SecondHand(t *testing.T) {
	state := twoPlayerState()

	// Run first hand setup.
	afterFirst, _, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}
	firstDealer := afterFirst.CurrentHand.DealerPlayerId

	// End the hand manually by folding p1 uncontested.
	afterFirst.Players["p1"].IsFolded = true
	afterFirst.CurrentHand.PendingActions = nil
	afterFirst.CurrentHand.ActingPlayerId = nil

	// Let advance finish the hand.
	afterHandEnd, _, err := advanceUntilQuiescent(afterFirst, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	if afterHandEnd.CurrentHand == nil {
		t.Fatal("expected new hand to have started")
	}
	secondDealer := afterHandEnd.CurrentHand.DealerPlayerId
	if firstDealer == secondDealer {
		t.Errorf("dealer did not rotate: both hands dealt by %s", firstDealer)
	}
}