package engine

import (
	"strings"
	"testing"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// ── card helpers ──────────────────────────────────────────────────────────────

// card builds a card value from suit (0=♣ 1=♦ 2=♥ 3=♠) and rank (0=2…12=A).
func card(suit, rank int) uint32 { return uint32(suit*13 + rank) }

// Rank constants (matching the encoding: 0=2 … 12=A).
const (
	r2, r3, r4, r5, r6, r7, r8, r9, rT, rJ, rQ, rK, rA = 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12
)

// Suit constants.
const (
	clubs, diamonds, hearts, spades = 0, 1, 2, 3
)

// ── eval5: hand category tests ────────────────────────────────────────────────

func TestEval5_RoyalFlush(t *testing.T) {
	cards := [5]uint32{
		card(spades, rT), card(spades, rJ), card(spades, rQ),
		card(spades, rK), card(spades, rA),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catStraightFlush {
		t.Errorf("category: want StraightFlush (royal), got %d", r>>60)
	}
	if desc != "Royal Flush" {
		t.Errorf("desc: got %q, want %q", desc, "Royal Flush")
	}
}

func TestEval5_StraightFlush_KingHigh(t *testing.T) {
	// 9-T-J-Q-K all hearts.
	cards := [5]uint32{
		card(hearts, r9), card(hearts, rT), card(hearts, rJ),
		card(hearts, rQ), card(hearts, rK),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catStraightFlush {
		t.Errorf("category: want StraightFlush, got %d", r>>60)
	}
	if !strings.Contains(desc, "King") {
		t.Errorf("desc %q should contain King", desc)
	}
}

func TestEval5_StraightFlush_Wheel(t *testing.T) {
	// A-2-3-4-5 all clubs (wheel straight flush, 5-high).
	cards := [5]uint32{
		card(clubs, rA), card(clubs, r2), card(clubs, r3),
		card(clubs, r4), card(clubs, r5),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catStraightFlush {
		t.Errorf("category: want StraightFlush, got %d", r>>60)
	}
	if !strings.Contains(desc, "5") {
		t.Errorf("desc %q should say 5-high", desc)
	}
}

func TestEval5_FourOfAKind(t *testing.T) {
	cards := [5]uint32{
		card(clubs, rA), card(diamonds, rA), card(hearts, rA), card(spades, rA),
		card(clubs, rK),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catFourOfAKind {
		t.Errorf("category: want FourOfAKind, got %d", r>>60)
	}
	if !strings.Contains(desc, "Aces") {
		t.Errorf("desc %q should mention Aces", desc)
	}
}

func TestEval5_FullHouse(t *testing.T) {
	// Aces full of Kings.
	cards := [5]uint32{
		card(clubs, rA), card(diamonds, rA), card(hearts, rA),
		card(clubs, rK), card(diamonds, rK),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catFullHouse {
		t.Errorf("category: want FullHouse, got %d", r>>60)
	}
	if !strings.Contains(desc, "Aces") || !strings.Contains(desc, "Kings") {
		t.Errorf("desc %q should mention Aces full of Kings", desc)
	}
}

func TestEval5_Flush(t *testing.T) {
	// A-J-9-7-5 all spades (no straight).
	cards := [5]uint32{
		card(spades, rA), card(spades, rJ), card(spades, r9),
		card(spades, r7), card(spades, r5),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catFlush {
		t.Errorf("category: want Flush, got %d", r>>60)
	}
	if !strings.Contains(desc, "Ace") {
		t.Errorf("desc %q should say Ace-high", desc)
	}
}

func TestEval5_Straight_AceHigh(t *testing.T) {
	// T-J-Q-K-A mixed suits.
	cards := [5]uint32{
		card(clubs, rT), card(diamonds, rJ), card(hearts, rQ),
		card(spades, rK), card(clubs, rA),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catStraight {
		t.Errorf("category: want Straight, got %d", r>>60)
	}
	if !strings.Contains(desc, "Ace") {
		t.Errorf("desc %q should say Ace-high", desc)
	}
}

func TestEval5_Straight_Wheel(t *testing.T) {
	// A-2-3-4-5 mixed suits (wheel, 5-high).
	cards := [5]uint32{
		card(clubs, rA), card(diamonds, r2), card(hearts, r3),
		card(spades, r4), card(clubs, r5),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catStraight {
		t.Errorf("category: want Straight, got %d", r>>60)
	}
	if !strings.Contains(desc, "5") {
		t.Errorf("desc %q should say 5-high", desc)
	}
	// Wheel must be weaker than a 6-high straight.
	sixHigh := [5]uint32{
		card(clubs, r2), card(diamonds, r3), card(hearts, r4),
		card(spades, r5), card(clubs, r6),
	}
	r6, _ := eval5(sixHigh)
	if r >= r6 {
		t.Error("wheel (5-high straight) must be weaker than 6-high straight")
	}
}

func TestEval5_ThreeOfAKind(t *testing.T) {
	cards := [5]uint32{
		card(clubs, rQ), card(diamonds, rQ), card(hearts, rQ),
		card(spades, rA), card(clubs, rK),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catThreeOfAKind {
		t.Errorf("category: want ThreeOfAKind, got %d", r>>60)
	}
	if !strings.Contains(desc, "Queens") {
		t.Errorf("desc %q should mention Queens", desc)
	}
}

func TestEval5_TwoPair(t *testing.T) {
	cards := [5]uint32{
		card(clubs, rA), card(diamonds, rA),
		card(hearts, rK), card(spades, rK),
		card(clubs, rQ),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catTwoPair {
		t.Errorf("category: want TwoPair, got %d", r>>60)
	}
	if !strings.Contains(desc, "Aces") || !strings.Contains(desc, "Kings") {
		t.Errorf("desc %q should mention Aces and Kings", desc)
	}
}

func TestEval5_OnePair(t *testing.T) {
	cards := [5]uint32{
		card(clubs, rA), card(diamonds, rA),
		card(hearts, rK), card(spades, rQ), card(clubs, rJ),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catOnePair {
		t.Errorf("category: want OnePair, got %d", r>>60)
	}
	if !strings.Contains(desc, "Aces") {
		t.Errorf("desc %q should mention Aces", desc)
	}
}

func TestEval5_HighCard(t *testing.T) {
	cards := [5]uint32{
		card(clubs, rA), card(diamonds, rK), card(hearts, rQ),
		card(spades, rJ), card(clubs, r9),
	}
	r, desc := eval5(cards)
	if handCat(r>>60) != catHighCard {
		t.Errorf("category: want HighCard, got %d", r>>60)
	}
	if !strings.Contains(desc, "Ace") {
		t.Errorf("desc %q should say Ace high", desc)
	}
}

// ── hand category ordering ────────────────────────────────────────────────────

func TestHandRank_CategoryOrder(t *testing.T) {
	// One representative of each category, ordered worst to best.
	hands := [][5]uint32{
		// High card: A K Q J 9 mixed
		{card(clubs, rA), card(diamonds, rK), card(hearts, rQ), card(spades, rJ), card(clubs, r9)},
		// Pair of Aces
		{card(clubs, rA), card(diamonds, rA), card(hearts, rK), card(spades, rQ), card(clubs, rJ)},
		// Two pair: AA KK
		{card(clubs, rA), card(diamonds, rA), card(hearts, rK), card(spades, rK), card(clubs, rQ)},
		// Three Aces
		{card(clubs, rA), card(diamonds, rA), card(hearts, rA), card(spades, rK), card(clubs, rQ)},
		// Straight: T-A (ace-high)
		{card(clubs, rT), card(diamonds, rJ), card(hearts, rQ), card(spades, rK), card(clubs, rA)},
		// Flush: A J 9 7 5 spades
		{card(spades, rA), card(spades, rJ), card(spades, r9), card(spades, r7), card(spades, r5)},
		// Full house: AAA KK
		{card(clubs, rA), card(diamonds, rA), card(hearts, rA), card(spades, rK), card(clubs, rK)},
		// Four Aces
		{card(clubs, rA), card(diamonds, rA), card(hearts, rA), card(spades, rA), card(clubs, rK)},
		// Royal flush (spades)
		{card(spades, rT), card(spades, rJ), card(spades, rQ), card(spades, rK), card(spades, rA)},
	}

	ranks := make([]handRank, len(hands))
	for i, h := range hands {
		ranks[i], _ = eval5(h)
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i] <= ranks[i-1] {
			t.Errorf("hand[%d] rank (%d) must be > hand[%d] rank (%d)", i, ranks[i], i-1, ranks[i-1])
		}
	}
}

// ── tiebreaker ordering ───────────────────────────────────────────────────────

func TestHandRank_Tiebreaker_HighCard(t *testing.T) {
	// A-K-Q-J-9 vs A-K-Q-J-8: first hand wins on 5th kicker.
	h1 := [5]uint32{card(clubs, rA), card(diamonds, rK), card(hearts, rQ), card(spades, rJ), card(clubs, r9)}
	h2 := [5]uint32{card(clubs, rA), card(diamonds, rK), card(hearts, rQ), card(spades, rJ), card(clubs, r8)}
	r1, _ := eval5(h1)
	r2, _ := eval5(h2)
	if r1 <= r2 {
		t.Error("A-K-Q-J-9 must beat A-K-Q-J-8")
	}
}

func TestHandRank_Tiebreaker_Pair_BetterKicker(t *testing.T) {
	// Pair of aces + K vs pair of aces + Q: K-kicker wins.
	h1 := [5]uint32{card(clubs, rA), card(diamonds, rA), card(hearts, rK), card(spades, r2), card(clubs, r3)}
	h2 := [5]uint32{card(clubs, rA), card(diamonds, rA), card(hearts, rQ), card(spades, r2), card(clubs, r3)}
	r1, _ := eval5(h1)
	r2, _ := eval5(h2)
	if r1 <= r2 {
		t.Error("pair of aces with K kicker must beat pair of aces with Q kicker")
	}
}

func TestHandRank_Tiebreaker_Straight_HigherBeatsLower(t *testing.T) {
	// Ace-high straight > King-high straight.
	aceHigh := [5]uint32{card(clubs, rT), card(diamonds, rJ), card(hearts, rQ), card(spades, rK), card(clubs, rA)}
	kingHigh := [5]uint32{card(clubs, r9), card(diamonds, rT), card(hearts, rJ), card(spades, rQ), card(clubs, rK)}
	rA, _ := eval5(aceHigh)
	rK, _ := eval5(kingHigh)
	if rA <= rK {
		t.Error("ace-high straight must beat king-high straight")
	}
}

// ── bestHand: best-from-7 selection ──────────────────────────────────────────

func TestBestHand_PicksBestFromSeven(t *testing.T) {
	// Hole cards: A♣ K♣
	// Board: Q♣ J♣ T♣ 2♦ 3♥
	// Best hand: royal flush (A K Q J T all clubs).
	c1 := card(clubs, rA)
	c2 := card(clubs, rK)
	board := []uint32{
		card(clubs, rQ), card(clubs, rJ), card(clubs, rT),
		card(diamonds, r2), card(hearts, r3),
	}
	r, _, desc := bestHand(c1, c2, board)
	if handCat(r>>60) != catStraightFlush {
		t.Errorf("expected Royal Flush, got category %d desc=%q", r>>60, desc)
	}
	if desc != "Royal Flush" {
		t.Errorf("desc: got %q, want Royal Flush", desc)
	}
}

func TestBestHand_PairVsHighCard(t *testing.T) {
	// Hole: A♠ A♥ (pair of aces) vs any high-card board.
	// Board: 2♣ 7♦ 9♥ J♠ K♣ — no flush/straight possible with these holes.
	c1 := card(spades, rA)
	c2 := card(hearts, rA)
	board := []uint32{
		card(clubs, r2), card(diamonds, r7), card(hearts, r9),
		card(spades, rJ), card(clubs, rK),
	}
	r, _, _ := bestHand(c1, c2, board)
	if handCat(r>>60) < catOnePair {
		t.Errorf("pair of aces in hole should produce at least one pair, got category %d", r>>60)
	}
}

func TestBestHand_FiveCardBoard_CanIgnoreHoleCards(t *testing.T) {
	// Board alone forms a royal flush; hole cards are trash.
	// Hole: 2♦ 3♦
	// Board: T♥ J♥ Q♥ K♥ A♥
	c1 := card(diamonds, r2)
	c2 := card(diamonds, r3)
	board := []uint32{
		card(hearts, rT), card(hearts, rJ), card(hearts, rQ),
		card(hearts, rK), card(hearts, rA),
	}
	r, _, desc := bestHand(c1, c2, board)
	if handCat(r>>60) != catStraightFlush {
		t.Errorf("expected Royal Flush using board, got category %d desc=%q", r>>60, desc)
	}
}

// ── endHandShowdown integration ───────────────────────────────────────────────

func TestShowdown_BetterHandWins(t *testing.T) {
	// p1: A♠ A♥ (pair of aces with board → two pair or better)
	// p2: 2♣ 3♦ (junk)
	// Board: A♣ K♦ K♥ J♠ 9♣
	// p1 best: AAA KK = full house; p2 best: pair of kings (K K J 9 3)
	// p1 must win.
	p1c1, p1c2 := uint32(card(spades, rA)), uint32(card(hearts, rA))
	p2c1, p2c2 := uint32(card(clubs, r2)), uint32(card(diamonds, r3))
	fc1, fc2, fc3 := uint32(card(clubs, rA)), uint32(card(diamonds, rK)), uint32(card(hearts, rK))
	tc, rc := uint32(card(spades, rJ)), uint32(card(clubs, r9))

	state := twoPlayerState()
	state.Status = 3 // ACTIVE
	state.Players["p1"].HoleCard_1 = &p1c1
	state.Players["p1"].HoleCard_2 = &p1c2
	state.Players["p2"].HoleCard_1 = &p2c1
	state.Players["p2"].HoleCard_2 = &p2c2
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_RIVER,
		Pot:            400,
		ActionOrder:    []string{"p1", "p2"},
		PendingActions: nil,
		ActingPlayerId: nil,
		FlopCard_1:     &fc1,
		FlopCard_2:     &fc2,
		FlopCard_3:     &fc3,
		TurnCard:       &tc,
		RiverCard:      &rc,
	}

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var awarded *pb.PotAwarded
	for _, e := range events {
		if p, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			awarded = p.PotAwarded
		}
	}
	if awarded == nil {
		t.Fatal("no PotAwarded event")
	}
	if len(awarded.WinnerPlayerIds) != 1 || awarded.WinnerPlayerIds[0] != "p1" {
		t.Errorf("winner: got %v, want [p1]", awarded.WinnerPlayerIds)
	}
	if awarded.HandDescription == "" {
		t.Error("HandDescription should be set")
	}
	if len(awarded.WinningHand) != 5 {
		t.Errorf("WinningHand: got %d bytes, want 5", len(awarded.WinningHand))
	}
}

func TestShowdown_TiedHandsSplitPot(t *testing.T) {
	// Both players have the same best hand (board plays — both have junk hole cards).
	// Board: A♣ K♦ Q♥ J♠ T♣ = royal-flush-on-board for both.
	// Hole: p1=2♦2♥, p2=3♦3♥ — board ace-high straight dominates both.
	p1c1, p1c2 := uint32(card(diamonds, r2)), uint32(card(hearts, r2))
	p2c1, p2c2 := uint32(card(diamonds, r3)), uint32(card(hearts, r3))
	fc1, fc2, fc3 := uint32(card(clubs, rA)), uint32(card(diamonds, rK)), uint32(card(hearts, rQ))
	tc, rc := uint32(card(spades, rJ)), uint32(card(clubs, rT))

	state := twoPlayerState()
	state.Status = 3
	state.Players["p1"].HoleCard_1 = &p1c1
	state.Players["p1"].HoleCard_2 = &p1c2
	state.Players["p2"].HoleCard_1 = &p2c1
	state.Players["p2"].HoleCard_2 = &p2c2
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_RIVER,
		Pot:            200,
		ActionOrder:    []string{"p1", "p2"},
		PendingActions: nil,
		ActingPlayerId: nil,
		FlopCard_1:     &fc1,
		FlopCard_2:     &fc2,
		FlopCard_3:     &fc3,
		TurnCard:       &tc,
		RiverCard:      &rc,
	}

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var awarded *pb.PotAwarded
	for _, e := range events {
		if p, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			awarded = p.PotAwarded
		}
	}
	if awarded == nil {
		t.Fatal("no PotAwarded event")
	}
	if len(awarded.WinnerPlayerIds) != 2 {
		t.Errorf("tied pot: want 2 winners, got %v", awarded.WinnerPlayerIds)
	}
	if awarded.Amount != 200 {
		t.Errorf("amount: got %d, want 200", awarded.Amount)
	}
}
