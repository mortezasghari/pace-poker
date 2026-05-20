package engine

import (
	"testing"
	"time"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/deck"
	"google.golang.org/protobuf/types/known/timestamppb"
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
// ── tests: action timeout ─────────────────────────────────────────────────────

func TestAdvance_Timeout_Folds_WhenBetActive(t *testing.T) {
	// p1 is acting; there is a live bet (CurrentHighestBet=100, p1.CurrentBet=0).
	// With an expired deadline, advance should emit PlayerTimedOut + PlayerFolded.
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	actingID := "p1"
	deadline := timestamppb.New(time.Now().Add(-1 * time.Second)) // already past
	state.CurrentHand = &pb.HandState{
		Phase:             pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:               150,
		CurrentHighestBet: 100,
		ActionOrder:       []string{"p1", "p2"},
		PendingActions:    []string{"p1"},
		ActingPlayerId:    &actingID,
		ActionDeadline:    deadline,
	}
	state.Players["p1"].CurrentBet = 0

	batch, err := advance(state, fixedDealer(), time.Now())
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("expected 2 events (TimedOut + Folded), got %d", len(batch))
	}
	if _, ok := batch[0].Event.(*pb.GameEvent_PlayerTimedOut); !ok {
		t.Errorf("event[0]: got %T, want PlayerTimedOut", batch[0].Event)
	}
	to := batch[0].GetPlayerTimedOut()
	if to.GetPlayerId() != "p1" {
		t.Errorf("TimedOut player: got %q, want p1", to.GetPlayerId())
	}
	if to.GetDefaultAction() != "fold" {
		t.Errorf("DefaultAction: got %q, want fold", to.GetDefaultAction())
	}
	if _, ok := batch[1].Event.(*pb.GameEvent_PlayerFolded); !ok {
		t.Errorf("event[1]: got %T, want PlayerFolded", batch[1].Event)
	}
}

func TestAdvance_Timeout_Checks_WhenNoBet(t *testing.T) {
	// p1 is acting; no live bet (CurrentHighestBet=0).
	// Default action should be check.
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	actingID := "p1"
	deadline := timestamppb.New(time.Now().Add(-1 * time.Second))
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_FLOP,
		Pot:            200,
		ActionOrder:    []string{"p1", "p2"},
		PendingActions: []string{"p1"},
		ActingPlayerId: &actingID,
		ActionDeadline: deadline,
	}
	// No current bet outstanding.

	batch, err := advance(state, fixedDealer(), time.Now())
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("expected 2 events (TimedOut + Checked), got %d", len(batch))
	}
	if _, ok := batch[0].Event.(*pb.GameEvent_PlayerTimedOut); !ok {
		t.Errorf("event[0]: got %T, want PlayerTimedOut", batch[0].Event)
	}
	if batch[0].GetPlayerTimedOut().GetDefaultAction() != "check" {
		t.Errorf("DefaultAction: got %q, want check", batch[0].GetPlayerTimedOut().GetDefaultAction())
	}
	if _, ok := batch[1].Event.(*pb.GameEvent_PlayerChecked); !ok {
		t.Errorf("event[1]: got %T, want PlayerChecked", batch[1].Event)
	}
}

func TestAdvance_NoTimeout_WhenDeadlineNotPassed(t *testing.T) {
	// Deadline is in the future — advance must not fire the timeout.
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	actingID := "p1"
	deadline := timestamppb.New(time.Now().Add(30 * time.Second)) // future
	state.CurrentHand = &pb.HandState{
		Phase:             pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:               150,
		CurrentHighestBet: 100,
		ActionOrder:       []string{"p1", "p2"},
		PendingActions:    []string{"p1"},
		ActingPlayerId:    &actingID,
		ActionDeadline:    deadline,
	}

	batch, err := advance(state, fixedDealer(), time.Now())
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	// Still waiting for the player → nil.
	if len(batch) != 0 {
		t.Errorf("expected nil batch (deadline not passed), got %d events: %T", len(batch), batch[0].Event)
	}
}

func TestAdvance_NoTimeout_WhenNowIsZero(t *testing.T) {
	// now=zero disables timeout check even if deadline is past (used in tests).
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	actingID := "p1"
	deadline := timestamppb.New(time.Now().Add(-1 * time.Second))
	state.CurrentHand = &pb.HandState{
		Phase:             pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:               150,
		CurrentHighestBet: 100,
		ActionOrder:       []string{"p1", "p2"},
		PendingActions:    []string{"p1"},
		ActingPlayerId:    &actingID,
		ActionDeadline:    deadline,
	}

	batch, err := advance(state, fixedDealer(), time.Time{}) // zero time
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(batch) != 0 {
		t.Errorf("expected nil batch with zero now, got %d events", len(batch))
	}
}

func TestAdvance_Timeout_ContinuesHandAfterFold(t *testing.T) {
	// After p1 times out and folds, only p2 remains → pot awarded in same advance loop.
	state := twoPlayerState()
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	actingID := "p1"
	deadline := timestamppb.New(time.Now().Add(-1 * time.Second))
	state.CurrentHand = &pb.HandState{
		Phase:             pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:               300,
		CurrentHighestBet: 150,
		ActionOrder:       []string{"p1", "p2"},
		PendingActions:    []string{"p1"},
		ActingPlayerId:    &actingID,
		ActionDeadline:    deadline,
	}
	state.Players["p1"].CurrentBet = 0

	// Use a real now so the expired deadline fires, then loop until quiescent.
	// Stop at HandEnded so a subsequent auto-start doesn't overwrite finalState.
	now := time.Now()
	var all []*pb.GameEvent
	cur := state
	done := false
	for range 64 {
		batch, err := advance(cur, fixedDealer(), now)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		cur = applyAll(cur, batch)
		all = append(all, batch...)
		// After first batch (timeout+fold), subsequent calls use zero now so
		// no second timeout fires — the loop just drives remaining lifecycle.
		now = time.Time{}
		for _, e := range batch {
			if _, ok := e.Event.(*pb.GameEvent_HandEnded); ok {
				done = true
			}
		}
		if done {
			break
		}
	}
	finalState := cur

	var timedOut, folded, potAwarded, handEnded bool
	for _, e := range all {
		switch e.Event.(type) {
		case *pb.GameEvent_PlayerTimedOut:
			timedOut = true
		case *pb.GameEvent_PlayerFolded:
			folded = true
		case *pb.GameEvent_PotAwarded:
			potAwarded = true
		case *pb.GameEvent_HandEnded:
			handEnded = true
		}
	}
	if !timedOut {
		t.Error("expected PlayerTimedOut event")
	}
	if !folded {
		t.Error("expected PlayerFolded event")
	}
	if !potAwarded {
		t.Error("expected PotAwarded event after uncontested")
	}
	if !handEnded {
		t.Error("expected HandEnded event")
	}
	if finalState.CurrentHand != nil {
		t.Error("hand should have ended")
	}
}
