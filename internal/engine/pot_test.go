package engine

import (
	"testing"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// potState builds a GameState suitable for computePots tests.
// players is a list of (id, committed, folded) tuples.
func potState(players []struct {
	id        string
	committed int64
	folded    bool
}) *pb.GameState {
	var order []string
	ps := make(map[string]*pb.PlayerState, len(players))
	for _, p := range players {
		ps[p.id] = &pb.PlayerState{
			PlayerId:               p.id,
			TotalCommittedThisHand: p.committed,
			IsFolded:               p.folded,
		}
		order = append(order, p.id)
	}
	return &pb.GameState{
		Players: ps,
		CurrentHand: &pb.HandState{
			ActionOrder: order,
		},
	}
}

// ── computePots ───────────────────────────────────────────────────────────────

func TestComputePots_SingleWinner(t *testing.T) {
	// Three players, no all-ins, one folds. Main pot only (eligible sets all equal → merged).
	state := potState([]struct {
		id        string
		committed int64
		folded    bool
	}{
		{"a", 100, true},  // folded early
		{"b", 300, false}, // called raise
		{"c", 300, false}, // called raise
	})

	pots := computePots(state)
	if len(pots) != 1 {
		t.Fatalf("expected 1 pot (merged), got %d", len(pots))
	}
	if pots[0].amount != 700 {
		t.Errorf("main pot: got %d, want 700", pots[0].amount)
	}
	if len(pots[0].eligibleIDs) != 2 {
		t.Errorf("main pot eligible: got %v, want [b c]", pots[0].eligibleIDs)
	}
}

func TestComputePots_OneAllIn_CreatesSidePot(t *testing.T) {
	// a all-in for 100; b and c called 300.
	// Main pot: 100*3 = 300, eligible [a, b, c]
	// Side pot: (300-100)*2 = 400, eligible [b, c]
	state := potState([]struct {
		id        string
		committed int64
		folded    bool
	}{
		{"a", 100, false}, // short stack all-in
		{"b", 300, false},
		{"c", 300, false},
	})

	pots := computePots(state)
	if len(pots) != 2 {
		t.Fatalf("expected 2 pots, got %d: %+v", len(pots), pots)
	}
	if pots[0].amount != 300 {
		t.Errorf("main pot: got %d, want 300", pots[0].amount)
	}
	if len(pots[0].eligibleIDs) != 3 {
		t.Errorf("main pot eligible count: got %d, want 3", len(pots[0].eligibleIDs))
	}
	if pots[1].amount != 400 {
		t.Errorf("side pot: got %d, want 400", pots[1].amount)
	}
	if len(pots[1].eligibleIDs) != 2 {
		t.Errorf("side pot eligible count: got %d, want 2", len(pots[1].eligibleIDs))
	}
	// Total must match.
	total := pots[0].amount + pots[1].amount
	if total != 700 {
		t.Errorf("total pots: got %d, want 700", total)
	}
}

func TestComputePots_TwoAllIns(t *testing.T) {
	// a all-in 50, b all-in 150, c called 300.
	// Level 50: 50*3=150, eligible [a, b, c]
	// Level 150: (150-50)*2=200, eligible [b, c]
	// Level 300: (300-150)*1=150, eligible [c]
	state := potState([]struct {
		id        string
		committed int64
		folded    bool
	}{
		{"a", 50, false},
		{"b", 150, false},
		{"c", 300, false},
	})

	pots := computePots(state)
	if len(pots) != 3 {
		t.Fatalf("expected 3 pots, got %d: %+v", len(pots), pots)
	}
	total := int64(0)
	for _, p := range pots {
		total += p.amount
	}
	if total != 500 {
		t.Errorf("total: got %d, want 500", total)
	}
	if pots[0].amount != 150 {
		t.Errorf("pot[0]: got %d, want 150", pots[0].amount)
	}
	if pots[1].amount != 200 {
		t.Errorf("pot[1]: got %d, want 200", pots[1].amount)
	}
	if pots[2].amount != 150 {
		t.Errorf("pot[2]: got %d, want 150", pots[2].amount)
	}
}

func TestComputePots_FoldedPlayerExcludedFromEligible(t *testing.T) {
	// a folded for 100; b and c each committed 300.
	// Merged into one pot (both levels have same eligible set [b, c]).
	state := potState([]struct {
		id        string
		committed int64
		folded    bool
	}{
		{"a", 100, true}, // folded — contributes chips but not eligible
		{"b", 300, false},
		{"c", 300, false},
	})

	pots := computePots(state)
	// Two raw pots: {300,[b,c]} and {400,[b,c]} — merged into {700,[b,c]}.
	if len(pots) != 1 {
		t.Fatalf("expected 1 merged pot, got %d: %+v", len(pots), pots)
	}
	if pots[0].amount != 700 {
		t.Errorf("pot amount: got %d, want 700", pots[0].amount)
	}
	for _, id := range pots[0].eligibleIDs {
		if id == "a" {
			t.Error("folded player 'a' must not be eligible")
		}
	}
}

func TestComputePots_AllInLessThanCall_FoldedExcluded(t *testing.T) {
	// a all-in 60 (short stack), b folded for 100, c called 300.
	// Level 60: 60*3=180, eligible [a, c]
	// Level 100: (100-60)*2=80, eligible [c]  (b folded so not eligible)
	// Level 300: (300-100)*1=200, eligible [c]
	// Pots [1] and [2] have same eligible [c] → merged to {280, [c]}
	state := potState([]struct {
		id        string
		committed int64
		folded    bool
	}{
		{"a", 60, false},  // short stack all-in
		{"b", 100, true},  // folded
		{"c", 300, false}, // caller
	})

	pots := computePots(state)
	total := int64(0)
	for _, p := range pots {
		total += p.amount
	}
	if total != 460 {
		t.Errorf("total: got %d, want 460 (60+100+300)", total)
	}
}

func TestComputePots_Empty(t *testing.T) {
	state := potState(nil)
	pots := computePots(state)
	if pots != nil {
		t.Errorf("expected nil for empty state, got %v", pots)
	}
}

// ── computeRake ───────────────────────────────────────────────────────────────

func TestComputeRake_NoConfig(t *testing.T) {
	if r := computeRake(1000, nil); r != 0 {
		t.Errorf("nil cfg: got %d, want 0", r)
	}
}

func TestComputeRake_NoBps(t *testing.T) {
	cfg := &pb.CashGameConfig{RakeBps: 0}
	if r := computeRake(1000, cfg); r != 0 {
		t.Errorf("zero bps: got %d, want 0", r)
	}
}

func TestComputeRake_Standard(t *testing.T) {
	// 5% rake (500 bps) on pot of 1000 → 50.
	cfg := &pb.CashGameConfig{RakeBps: 500}
	if r := computeRake(1000, cfg); r != 50 {
		t.Errorf("got %d, want 50", r)
	}
}

func TestComputeRake_CappedByRakeCap(t *testing.T) {
	// 10% rake (1000 bps) on pot of 1000 → 100, but cap is 30.
	cfg := &pb.CashGameConfig{RakeBps: 1000, RakeCap: 30}
	if r := computeRake(1000, cfg); r != 30 {
		t.Errorf("got %d, want 30 (capped)", r)
	}
}

// ── integration: endHandShowdown with side pots ───────────────────────────────

func TestAdvance_Showdown_SidePotCreatedAndAwarded(t *testing.T) {
	// a (all-in 100), b (300), c (300), pot=700. Expect 2 pots.
	state := twoPlayerState() // base state, we'll extend
	state.Players["p3"] = &pb.PlayerState{PlayerId: "p3", Seat: 2, Stack: 0, IsAllIn: true, TotalCommittedThisHand: 100}
	state.Players["p1"].TotalCommittedThisHand = 300
	state.Players["p2"].TotalCommittedThisHand = 300

	river := uint32(5)
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	state.CurrentHand = &pb.HandState{
		Phase:          pb.HandPhase_HAND_PHASE_RIVER,
		Pot:            700,
		ActionOrder:    []string{"p3", "p1", "p2"},
		PendingActions: nil,
		ActingPlayerId: nil,
		RiverCard:      &river,
	}

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var sidePotCount int
	var potAwardedTotal int64
	for _, e := range events {
		if _, ok := e.Event.(*pb.GameEvent_SidePotCreated); ok {
			sidePotCount++
		}
		if p, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			potAwardedTotal += p.PotAwarded.Amount
		}
	}

	if sidePotCount != 1 {
		t.Errorf("expected 1 SidePotCreated, got %d", sidePotCount)
	}
	if potAwardedTotal != 700 {
		t.Errorf("total awarded: got %d, want 700", potAwardedTotal)
	}
}

func TestAdvance_Uncontested_WithRake(t *testing.T) {
	// 5% rake (500 bps), pot=200, expected rake=10, award=190.
	state := twoPlayerState()
	state.Config.RakeBps = 500
	state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
	state.CurrentHand = &pb.HandState{
		Phase:       pb.HandPhase_HAND_PHASE_PREFLOP,
		Pot:         200,
		ActionOrder: []string{"p1", "p2"},
	}
	state.Players["p1"].IsFolded = true

	_, events, err := advanceUntilQuiescent(state, fixedDealer())
	if err != nil {
		t.Fatal(err)
	}

	var rake int64
	var awarded int64
	for _, e := range events {
		if r, ok := e.Event.(*pb.GameEvent_RakeTaken); ok {
			rake = r.RakeTaken.Amount
		}
		if p, ok := e.Event.(*pb.GameEvent_PotAwarded); ok {
			awarded = p.PotAwarded.Amount
		}
	}
	if rake != 10 {
		t.Errorf("rake: got %d, want 10", rake)
	}
	if awarded != 190 {
		t.Errorf("awarded: got %d, want 190", awarded)
	}
}