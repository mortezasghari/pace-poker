package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// advance examines state and returns the next batch of server-driven events,
// or nil if the engine is waiting for a player action. now is injected for
// ActionStarted deadlines and may be zero (no deadline set).
//
// It is a pure function except for the Dealer (randomness source).
func advance(state *pb.GameState, dlr Dealer, now time.Time) ([]*pb.GameEvent, error) {
	if state.CurrentHand == nil {
		return advanceIdle(state, dlr)
	}
	return advanceHand(state, dlr, now)
}

// advanceIdle decides whether to start a new hand.
func advanceIdle(state *pb.GameState, dlr Dealer) ([]*pb.GameEvent, error) {
	switch state.Status {
	case pb.GameStatus_GAME_STATUS_WAITING, pb.GameStatus_GAME_STATUS_ACTIVE:
	default:
		return nil, nil
	}

	// Auto-sat-out any player who busted (stack == 0, not already sitting out).
	var satOutEvents []*pb.GameEvent
	for _, p := range state.Players {
		if p.Stack == 0 && !p.IsSittingOut {
			satOutEvents = append(satOutEvents, &pb.GameEvent{
				Event: &pb.GameEvent_PlayerSatOut{PlayerSatOut: &pb.PlayerSatOut{PlayerId: p.PlayerId}},
			})
		}
	}
	if len(satOutEvents) > 0 {
		return satOutEvents, nil
	}

	active := activePlayers(state)
	minP := state.GetConfig().GetMinPlayersToStart()
	if minP < 2 {
		minP = 2
	}
	if int32(len(active)) < minP {
		return nil, nil
	}
	return startNewHand(state, dlr, active)
}

// advanceHand progresses an in-progress hand.
func advanceHand(state *pb.GameState, dlr Dealer, now time.Time) ([]*pb.GameEvent, error) {
	h := state.CurrentHand

	// Uncontested: all but one player folded.
	if nonFoldedCount(state) <= 1 {
		return endHandUncontested(state)
	}

	// Betting round finished.
	if len(h.PendingActions) == 0 && h.ActingPlayerId == nil {
		return advanceStreet(state, dlr)
	}

	// Waiting for the acting player.
	if h.ActingPlayerId != nil {
		return nil, nil
	}

	// PendingActions not empty, no actor set yet → emit ActionStarted.
	return startNextAction(state, now)
}

// TODO(must-post-blind): before startNewHand, check MustPostBlind on each player and
// collect dead blind postings from returning players.

// startNewHand emits all events required to begin a new hand:
// HandStarted → AntePosted × N → BlindPosted (SB, BB) → HoleCardsDealt/Revealed × N.
// ActionStarted is NOT emitted here; advanceHand emits it on the next iteration.
func startNewHand(state *pb.GameState, dlr Dealer, active []*pb.PlayerState) ([]*pb.GameEvent, error) {
	n := len(active)
	cfg := state.GetConfig()

	dIdx := newDealerIdx(active, state.DealerSeat, state.HandNumber)
	var sbIdx, bbIdx int
	if n == 2 {
		// Heads-up: dealer is SB.
		sbIdx = dIdx
		bbIdx = (dIdx + 1) % n
	} else {
		sbIdx = (dIdx + 1) % n
		bbIdx = (dIdx + 2) % n
	}
	dealer := active[dIdx]
	sb := active[sbIdx]
	bb := active[bbIdx]

	if err := dlr.Shuffle(); err != nil {
		return nil, fmt.Errorf("shuffle: %w", err)
	}

	handID := uuid.New().String()
	handNumber := state.HandNumber + 1
	var events []*pb.GameEvent

	events = append(events, &pb.GameEvent{Event: &pb.GameEvent_HandStarted{HandStarted: &pb.HandStarted{
		HandId:         handID,
		HandNumber:     handNumber,
		DealerPlayerId: dealer.PlayerId,
		SbPlayerId:     sb.PlayerId,
		BbPlayerId:     bb.PlayerId,
		SmallBlind:     cfg.GetSmallBlind(),
		BigBlind:       cfg.GetBigBlind(),
	}}})

	// Antes (regular ante: every player posts).
	if ante := cfg.GetAnte(); ante > 0 {
		for _, p := range active {
			amount := min(ante, p.Stack)
			events = append(events, &pb.GameEvent{Event: &pb.GameEvent_AntePosted{AntePosted: &pb.AntePosted{
				PlayerId: p.PlayerId,
				Amount:   amount,
			}}})
		}
	}
	// BB ante (only the big blind posts).
	if bbAnte := cfg.GetBbAnte(); bbAnte > 0 {
		amount := min(bbAnte, bb.Stack)
		events = append(events, &pb.GameEvent{Event: &pb.GameEvent_AntePosted{AntePosted: &pb.AntePosted{
			PlayerId: bb.PlayerId,
			Amount:   amount,
		}}})
	}

	// Blinds.
	sbAmount := min(cfg.GetSmallBlind(), sb.Stack)
	events = append(events, &pb.GameEvent{Event: &pb.GameEvent_BlindPosted{BlindPosted: &pb.BlindPosted{
		PlayerId:     sb.PlayerId,
		Amount:       sbAmount,
		IsSmallBlind: true,
	}}})
	bbAmount := min(cfg.GetBigBlind(), bb.Stack)
	events = append(events, &pb.GameEvent{Event: &pb.GameEvent_BlindPosted{BlindPosted: &pb.BlindPosted{
		PlayerId:   bb.PlayerId,
		Amount:     bbAmount,
		IsBigBlind: true,
	}}})

	// Hole cards: 2 per player in seat order (HoleCardsDealt is broadcast, HoleCardsRevealed is private).
	for _, p := range active {
		c1, err := dlr.Deal()
		if err != nil {
			return nil, fmt.Errorf("deal to %s card 1: %w", p.PlayerId, err)
		}
		c2, err := dlr.Deal()
		if err != nil {
			return nil, fmt.Errorf("deal to %s card 2: %w", p.PlayerId, err)
		}
		events = append(events,
			&pb.GameEvent{Event: &pb.GameEvent_HoleCardsDealt{HoleCardsDealt: &pb.HoleCardsDealt{PlayerId: p.PlayerId}}},
			&pb.GameEvent{Event: &pb.GameEvent_HoleCardsRevealed{HoleCardsRevealed: &pb.HoleCardsRevealed{
				PlayerId: p.PlayerId,
				Card_1:   uint32(c1),
				Card_2:   uint32(c2),
			}}},
		)
	}

	return events, nil
}

// advanceStreet emits BettingRoundEnded + the next board card(s), or triggers
// the showdown when the river betting is complete.
func advanceStreet(state *pb.GameState, dlr Dealer) ([]*pb.GameEvent, error) {
	h := state.CurrentHand
	bre := &pb.GameEvent{Event: &pb.GameEvent_BettingRoundEnded{BettingRoundEnded: &pb.BettingRoundEnded{
		Phase:    h.Phase,
		PotTotal: h.Pot,
	}}}

	switch h.Phase {
	case pb.HandPhase_HAND_PHASE_PREFLOP:
		if err := dlr.Burn(); err != nil {
			return nil, fmt.Errorf("burn (flop): %w", err)
		}
		c1, err := dlr.Deal()
		if err != nil {
			return nil, err
		}
		c2, err := dlr.Deal()
		if err != nil {
			return nil, err
		}
		c3, err := dlr.Deal()
		if err != nil {
			return nil, err
		}
		return []*pb.GameEvent{bre, {Event: &pb.GameEvent_FlopDealt{FlopDealt: &pb.FlopDealt{
			Card_1: uint32(c1),
			Card_2: uint32(c2),
			Card_3: uint32(c3),
		}}}}, nil

	case pb.HandPhase_HAND_PHASE_FLOP:
		if err := dlr.Burn(); err != nil {
			return nil, fmt.Errorf("burn (turn): %w", err)
		}
		c, err := dlr.Deal()
		if err != nil {
			return nil, err
		}
		return []*pb.GameEvent{bre, {Event: &pb.GameEvent_TurnDealt{TurnDealt: &pb.TurnDealt{Card: uint32(c)}}}}, nil

	case pb.HandPhase_HAND_PHASE_TURN:
		if err := dlr.Burn(); err != nil {
			return nil, fmt.Errorf("burn (river): %w", err)
		}
		c, err := dlr.Deal()
		if err != nil {
			return nil, err
		}
		return []*pb.GameEvent{bre, {Event: &pb.GameEvent_RiverDealt{RiverDealt: &pb.RiverDealt{Card: uint32(c)}}}}, nil

	case pb.HandPhase_HAND_PHASE_RIVER:
		showdownEvts, err := endHandShowdown(state)
		if err != nil {
			return nil, err
		}
		return append([]*pb.GameEvent{bre}, showdownEvts...), nil

	default:
		return nil, nil
	}
}

// endHandUncontested awards the pot to the sole remaining (non-folded) player.
func endHandUncontested(state *pb.GameState) ([]*pb.GameEvent, error) {
	h := state.CurrentHand
	var winnerID string
	for _, pid := range h.ActionOrder {
		p := state.Players[pid]
		if p != nil && !p.IsFolded {
			winnerID = pid
			break
		}
	}
	if winnerID == "" {
		return nil, nil
	}

	var events []*pb.GameEvent
	cfg := state.GetConfig()
	rake := computeRake(h.Pot, cfg)
	if rake > 0 {
		events = append(events, &pb.GameEvent{Event: &pb.GameEvent_RakeTaken{
			RakeTaken: &pb.RakeTaken{Amount: rake},
		}})
	}
	events = append(events,
		&pb.GameEvent{Event: &pb.GameEvent_PotAwarded{PotAwarded: &pb.PotAwarded{
			Amount:          h.Pot - rake,
			WinnerPlayerIds: []string{winnerID},
		}}},
		&pb.GameEvent{Event: &pb.GameEvent_HandEnded{HandEnded: &pb.HandEnded{
			HandNumber: state.HandNumber + 1,
		}}},
	)
	return events, nil
}

// endHandShowdown emits Showdown + SidePotCreated(s) + RakeTaken + PotAwarded(s) + HandEnded.
// TODO(hand-evaluator): replace stub winner selection with real 5-card hand evaluator.
// TODO(run-it-twice): check RunItTwiceAgreed and deal second board before showdown.
func endHandShowdown(state *pb.GameState) ([]*pb.GameEvent, error) {
	h := state.CurrentHand
	nonFolded := nonFoldedPlayerIDs(state)
	if len(nonFolded) == 0 {
		return nil, nil
	}

	pots := computePots(state)
	if len(pots) == 0 {
		// Fallback for states where TotalCommittedThisHand is not populated (e.g. tests).
		pots = []pot{{amount: h.Pot, eligibleIDs: nonFolded}}
	}

	var events []*pb.GameEvent
	events = append(events, &pb.GameEvent{Event: &pb.GameEvent_Showdown{Showdown: &pb.Showdown{
		PlayerIdsInOrder: nonFolded,
	}}})

	// Announce side pots (all pots after the first/main pot).
	for _, p := range pots[1:] {
		events = append(events, &pb.GameEvent{Event: &pb.GameEvent_SidePotCreated{
			SidePotCreated: &pb.SidePotCreated{
				Amount:            p.amount,
				EligiblePlayerIds: p.eligibleIDs,
			},
		}})
	}

	// Rake from main pot.
	cfg := state.GetConfig()
	rake := computeRake(h.Pot, cfg)
	if rake > 0 {
		events = append(events, &pb.GameEvent{Event: &pb.GameEvent_RakeTaken{
			RakeTaken: &pb.RakeTaken{Amount: rake},
		}})
		pots[0].amount -= rake
	}

	// Award each pot to the stub winner (first eligible non-folded in ActionOrder).
	for _, p := range pots {
		winner := firstEligibleInOrder(state, p.eligibleIDs)
		if winner == "" || p.amount <= 0 {
			continue
		}
		events = append(events, &pb.GameEvent{Event: &pb.GameEvent_PotAwarded{PotAwarded: &pb.PotAwarded{
			Amount:          p.amount,
			WinnerPlayerIds: []string{winner},
		}}})
	}

	events = append(events, &pb.GameEvent{Event: &pb.GameEvent_HandEnded{HandEnded: &pb.HandEnded{
		HandNumber: state.HandNumber + 1,
	}}})
	return events, nil
}

// TODO(timeout): if ActingPlayerId is set and ActionDeadline has passed, emit PlayerTimedOut
// and apply a default action (fold if there's a bet, check otherwise).

// startNextAction emits ActionStarted for the first player in PendingActions.
func startNextAction(state *pb.GameState, now time.Time) ([]*pb.GameEvent, error) {
	h := state.CurrentHand
	pid := h.PendingActions[0]
	cfg := state.GetConfig()

	timeout := cfg.GetActionTimeout()
	var deadline *timestamppb.Timestamp
	if timeout != nil && timeout.AsDuration() > 0 && !now.IsZero() {
		deadline = timestamppb.New(now.Add(timeout.AsDuration()))
	}

	return []*pb.GameEvent{{Event: &pb.GameEvent_ActionStarted{ActionStarted: &pb.ActionStarted{
		PlayerId:  pid,
		TimeToAct: timeout,
		Deadline:  deadline,
	}}}}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// activePlayers returns seated, non-sitting-out players with stack > 0, sorted by seat.
func activePlayers(state *pb.GameState) []*pb.PlayerState {
	var out []*pb.PlayerState
	for _, p := range state.Players {
		if !p.IsSittingOut && p.Stack > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seat < out[j].Seat })
	return out
}

// newDealerIdx returns the index in active of the next dealer.
// For the first hand (handNumber == 0) the player with the lowest seat is chosen.
// Subsequent hands advance clockwise past the previous dealer seat.
func newDealerIdx(active []*pb.PlayerState, dealerSeat int32, handNumber int64) int {
	if handNumber == 0 {
		return 0
	}
	for i, p := range active {
		if p.Seat > dealerSeat {
			return i
		}
	}
	return 0 // wrap: new dealer is lowest seat
}

// nonFoldedCount returns how many players in the current hand have not folded.
func nonFoldedCount(state *pb.GameState) int {
	if state.CurrentHand == nil {
		return 0
	}
	n := 0
	for _, pid := range state.CurrentHand.ActionOrder {
		p := state.Players[pid]
		if p != nil && !p.IsFolded {
			n++
		}
	}
	return n
}

// nonFoldedPlayerIDs returns IDs of non-folded players in ActionOrder order.
func nonFoldedPlayerIDs(state *pb.GameState) []string {
	if state.CurrentHand == nil {
		return nil
	}
	var out []string
	for _, pid := range state.CurrentHand.ActionOrder {
		p := state.Players[pid]
		if p != nil && !p.IsFolded {
			out = append(out, pid)
		}
	}
	return out
}