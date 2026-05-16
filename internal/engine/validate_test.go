package engine

import (
	"strings"
	"testing"
	"time"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	actorP1 = "player-1"
	actorP2 = "player-2"
	bb      = int64(100)
	sb      = int64(50)
)

// newTestState builds a representative state with:
//   - ACTIVE status, no-limit, BB=100
//   - one hand in progress at PREFLOP
//   - P1 is the acting player (stack 10000, current_bet=100)
//   - P2 is seated (stack 9950, current_bet=100 as the BB)
//   - pot=200, current_highest_bet=100
func newTestState() *pb.GameState {
	actorID := actorP1
	p1Stack := int64(10000)
	p1Bet := int64(0) // SB posted 50, hasn't called yet; simplify: P1 is UTG with current_bet=0
	return &pb.GameState{
		GameId: "game-1",
		Status: pb.GameStatus_GAME_STATUS_ACTIVE,
		Config: &pb.CashGameConfig{
			BigBlind:        bb,
			SmallBlind:      sb,
			Structure:       pb.BettingStructure_BETTING_STRUCTURE_NO_LIMIT,
			AllowChat:       true,
			AllowRunItTwice: true,
		},
		Players: map[string]*pb.PlayerState{
			actorP1: {
				PlayerId:   actorP1,
				Seat:       1,
				SeatStatus: pb.SeatStatus_SEAT_STATUS_OCCUPIED,
				Stack:      p1Stack,
				CurrentBet: p1Bet,
			},
			actorP2: {
				PlayerId:   actorP2,
				Seat:       2,
				SeatStatus: pb.SeatStatus_SEAT_STATUS_OCCUPIED,
				Stack:      int64(9900),
				CurrentBet: bb, // P2 posted BB
			},
		},
		CurrentHand: &pb.HandState{
			HandId:             "hand-1",
			Phase:              pb.HandPhase_HAND_PHASE_PREFLOP,
			CurrentHighestBet:  bb,
			LastRaiseAmount:    bb,
			MinRaiseAmount:     bb,
			ActingPlayerId:     &actorID,
			ActionDeadline:     timestamppb.Now(),
			Pot:                bb + sb,
			SmallBlindPlayerId: actorP1,
			BigBlindPlayerId:   actorP2,
		},
	}
}

// cloneState makes a shallow-enough copy for in-test modifications.
func cloneState(s *pb.GameState) *pb.GameState {
	c := *s
	c.Players = make(map[string]*pb.PlayerState, len(s.Players))
	for k, v := range s.Players {
		cp := *v
		c.Players[k] = &cp
	}
	if s.CurrentHand != nil {
		h := *s.CurrentHand
		c.CurrentHand = &h
	}
	cfg := *s.Config
	c.Config = &cfg
	return &c
}

func withStatus(s *pb.GameState, status pb.GameStatus) *pb.GameState {
	c := cloneState(s)
	c.Status = status
	return c
}

func withNoHand(s *pb.GameState) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand = nil
	return c
}

func withPhase(s *pb.GameState, phase pb.HandPhase) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand.Phase = phase
	return c
}

func withActor(s *pb.GameState, id string) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand.ActingPlayerId = &id
	return c
}

func withNoActor(s *pb.GameState) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand.ActingPlayerId = nil
	return c
}

func withPlayerFolded(s *pb.GameState, id string) *pb.GameState {
	c := cloneState(s)
	p := *c.Players[id]
	p.IsFolded = true
	c.Players[id] = &p
	return c
}

func withPlayerAllIn(s *pb.GameState, id string) *pb.GameState {
	c := cloneState(s)
	p := *c.Players[id]
	p.IsAllIn = true
	c.Players[id] = &p
	return c
}

func withPlayerSittingOut(s *pb.GameState, id string) *pb.GameState {
	c := cloneState(s)
	p := *c.Players[id]
	p.IsSittingOut = true
	c.Players[id] = &p
	return c
}

func withPlayerStack(s *pb.GameState, id string, stack int64) *pb.GameState {
	c := cloneState(s)
	p := *c.Players[id]
	p.Stack = stack
	c.Players[id] = &p
	return c
}

func withCurrentBet(s *pb.GameState, highestBet int64) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand.CurrentHighestBet = highestBet
	return c
}

func withRaisesThisStreet(s *pb.GameState, n int32) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand.RaisesThisStreet = n
	return c
}

func withLastRaiseAmount(s *pb.GameState, amount int64) *pb.GameState {
	c := cloneState(s)
	c.CurrentHand.LastRaiseAmount = amount
	return c
}

func assertNil(t *testing.T, r *RejectionReason) {
	t.Helper()
	if r != nil {
		t.Errorf("expected nil rejection, got code=%q reason=%q", r.Code, r.Reason)
	}
}

func assertCode(t *testing.T, r *RejectionReason, want RejectionCode) {
	t.Helper()
	if r == nil {
		t.Errorf("expected rejection %q, got nil", want)
		return
	}
	if r.Code != want {
		t.Errorf("code: got %q, want %q (reason: %s)", r.Code, want, r.Reason)
	}
}

// ── Fold ──────────────────────────────────────────────────────────────────────

func TestValidateFold(t *testing.T) {
	fold := &pb.FoldCommand{}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateFold(newTestState(), fold, actorP1))
	})
	t.Run("not_seated", func(t *testing.T) {
		assertCode(t, validateFold(newTestState(), fold, "stranger"), CodeNotAtTable)
	})
	t.Run("not_your_turn", func(t *testing.T) {
		assertCode(t, validateFold(withActor(newTestState(), actorP2), fold, actorP1), CodeNotYourTurn)
	})
	t.Run("already_folded", func(t *testing.T) {
		assertCode(t, validateFold(withPlayerFolded(newTestState(), actorP1), fold, actorP1), CodePlayerFolded)
	})
	t.Run("all_in", func(t *testing.T) {
		assertCode(t, validateFold(withPlayerAllIn(newTestState(), actorP1), fold, actorP1), CodePlayerAllIn)
	})
	t.Run("game_not_active", func(t *testing.T) {
		assertCode(t, validateFold(withStatus(newTestState(), pb.GameStatus_GAME_STATUS_WAITING), fold, actorP1), CodeGameNotActive)
	})
	t.Run("no_hand", func(t *testing.T) {
		assertCode(t, validateFold(withNoHand(withStatus(newTestState(), pb.GameStatus_GAME_STATUS_ACTIVE)), fold, actorP1), CodeHandNotInProgress)
	})
}

// ── Check ─────────────────────────────────────────────────────────────────────

func TestValidateCheck(t *testing.T) {
	check := &pb.CheckCommand{}

	// Build a state where current_highest_bet == P1's current_bet (nothing to call).
	stateNoCall := func() *pb.GameState {
		s := cloneState(newTestState())
		s.CurrentHand.CurrentHighestBet = 0
		s.Players[actorP1].CurrentBet = 0
		return s
	}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateCheck(stateNoCall(), check, actorP1))
	})
	t.Run("bet_present", func(t *testing.T) {
		// Default state has current_highest_bet=BB and P1's current_bet=0, so there's a call.
		assertCode(t, validateCheck(newTestState(), check, actorP1), CodeCannotCheck)
	})
	t.Run("table_closed", func(t *testing.T) {
		assertCode(t, validateCheck(withStatus(stateNoCall(), pb.GameStatus_GAME_STATUS_CLOSED), check, actorP1), CodeTableClosed)
	})
	t.Run("not_your_turn", func(t *testing.T) {
		assertCode(t, validateCheck(withActor(stateNoCall(), actorP2), check, actorP1), CodeNotYourTurn)
	})
	t.Run("sitting_out", func(t *testing.T) {
		assertCode(t, validateCheck(withPlayerSittingOut(stateNoCall(), actorP1), check, actorP1), CodePlayerSittingOut)
	})
}

// ── Call ──────────────────────────────────────────────────────────────────────

func TestValidateCall(t *testing.T) {
	t.Run("valid_no_amount", func(t *testing.T) {
		cmd := &pb.CallCommand{}
		assertNil(t, validateCall(newTestState(), cmd, actorP1))
	})
	t.Run("valid_matching_amount", func(t *testing.T) {
		// P1 current_bet=0, highest=100, needed=100.
		cmd := &pb.CallCommand{Amount: bb}
		assertNil(t, validateCall(newTestState(), cmd, actorP1))
	})
	t.Run("nothing_to_call", func(t *testing.T) {
		s := cloneState(newTestState())
		s.CurrentHand.CurrentHighestBet = 0
		s.Players[actorP1].CurrentBet = 0
		assertCode(t, validateCall(s, &pb.CallCommand{}, actorP1), CodeIllegalAction)
	})
	t.Run("amount_mismatch", func(t *testing.T) {
		cmd := &pb.CallCommand{Amount: bb + 1}
		assertCode(t, validateCall(newTestState(), cmd, actorP1), CodeCallAmountMismatch)
	})
	t.Run("not_seated", func(t *testing.T) {
		assertCode(t, validateCall(newTestState(), &pb.CallCommand{}, "ghost"), CodeNotAtTable)
	})
	t.Run("not_your_turn", func(t *testing.T) {
		assertCode(t, validateCall(withActor(newTestState(), actorP2), &pb.CallCommand{}, actorP1), CodeNotYourTurn)
	})
}

// ── Bet ───────────────────────────────────────────────────────────────────────

func TestValidateBet(t *testing.T) {
	// Build a state with no existing bet (flop-like: current_highest_bet=0).
	noBetState := func() *pb.GameState {
		s := cloneState(newTestState())
		s.CurrentHand.CurrentHighestBet = 0
		s.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_FLOP
		s.Players[actorP1].CurrentBet = 0
		s.Players[actorP2].CurrentBet = 0
		return s
	}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateBet(noBetState(), &pb.BetCommand{Amount: bb}, actorP1))
	})
	t.Run("already_a_bet", func(t *testing.T) {
		assertCode(t, validateBet(newTestState(), &pb.BetCommand{Amount: bb}, actorP1), CodeIllegalAction)
	})
	t.Run("zero_amount", func(t *testing.T) {
		assertCode(t, validateBet(noBetState(), &pb.BetCommand{Amount: 0}, actorP1), CodeIllegalAmount)
	})
	t.Run("below_min_bet", func(t *testing.T) {
		assertCode(t, validateBet(noBetState(), &pb.BetCommand{Amount: bb - 1}, actorP1), CodeAmountBelowMinBet)
	})
	t.Run("below_min_but_all_in", func(t *testing.T) {
		// Stack equals the sub-minimum amount — going all-in is allowed.
		s := withPlayerStack(noBetState(), actorP1, bb-1)
		assertNil(t, validateBet(s, &pb.BetCommand{Amount: bb - 1}, actorP1))
	})
	t.Run("above_stack", func(t *testing.T) {
		assertCode(t, validateBet(noBetState(), &pb.BetCommand{Amount: 99999}, actorP1), CodeAmountAboveStack)
	})
}

// ── Raise ─────────────────────────────────────────────────────────────────────

func TestValidateRaise(t *testing.T) {
	// Default state: current_highest_bet=100, last_raise=100 → minRaiseTo=200.
	// P1 current_bet=0, stack=10000.
	minTo := bb + bb // 200

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateRaise(newTestState(), &pb.RaiseCommand{To: minTo}, actorP1))
	})
	t.Run("no_bet_to_raise", func(t *testing.T) {
		s := withCurrentBet(newTestState(), 0)
		assertCode(t, validateRaise(s, &pb.RaiseCommand{To: bb}, actorP1), CodeIllegalAction)
	})
	t.Run("to_not_exceeding_current", func(t *testing.T) {
		assertCode(t, validateRaise(newTestState(), &pb.RaiseCommand{To: bb}, actorP1), CodeIllegalAmount)
	})
	t.Run("above_stack", func(t *testing.T) {
		s := withPlayerStack(newTestState(), actorP1, 50)
		// needed = To - p.CurrentBet = 200 - 0 = 200, stack = 50
		assertCode(t, validateRaise(s, &pb.RaiseCommand{To: minTo}, actorP1), CodeAmountAboveStack)
	})
	t.Run("below_min_not_all_in", func(t *testing.T) {
		// Raise to 150 (below minTo=200), stack=10000 (not all-in).
		assertCode(t, validateRaise(newTestState(), &pb.RaiseCommand{To: 150}, actorP1), CodeAmountBelowMinRaise)
	})
	t.Run("below_min_but_all_in", func(t *testing.T) {
		// Stack = needed for a 150 raise (150 - currentBet=0 = 150); going all-in.
		s := withPlayerStack(newTestState(), actorP1, 150)
		assertNil(t, validateRaise(s, &pb.RaiseCommand{To: 150}, actorP1))
	})
	t.Run("fixed_limit_cap_reached", func(t *testing.T) {
		s := cloneState(newTestState())
		s.Config.Structure = pb.BettingStructure_BETTING_STRUCTURE_FIXED_LIMIT
		s.Config.MaxRaisesPerStreet = 3
		s = withRaisesThisStreet(s, 3)
		assertCode(t, validateRaise(s, &pb.RaiseCommand{To: minTo}, actorP1), CodeRaiseCapReached)
	})
	t.Run("fixed_limit_uncap_heads_up", func(t *testing.T) {
		s := cloneState(newTestState())
		s.Config.Structure = pb.BettingStructure_BETTING_STRUCTURE_FIXED_LIMIT
		s.Config.MaxRaisesPerStreet = 3
		s.Config.UncapHeadsUp = true
		// Only 2 players in state.
		s = withRaisesThisStreet(s, 10)
		assertNil(t, validateRaise(s, &pb.RaiseCommand{To: minTo}, actorP1))
	})
}

// ── AllIn ─────────────────────────────────────────────────────────────────────

func TestValidateAllIn(t *testing.T) {
	allin := &pb.AllInCommand{}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateAllIn(newTestState(), allin, actorP1))
	})
	t.Run("zero_stack", func(t *testing.T) {
		s := withPlayerStack(newTestState(), actorP1, 0)
		assertCode(t, validateAllIn(s, allin, actorP1), CodeIllegalAction)
	})
}

// ── ShowCards ─────────────────────────────────────────────────────────────────

func TestValidateShowCards(t *testing.T) {
	show := &pb.ShowCardsCommand{}

	t.Run("valid_at_showdown", func(t *testing.T) {
		assertNil(t, validateShowCards(withPhase(newTestState(), pb.HandPhase_HAND_PHASE_SHOWDOWN), show, actorP1))
	})
	t.Run("valid_at_hand_over", func(t *testing.T) {
		assertNil(t, validateShowCards(withPhase(newTestState(), pb.HandPhase_HAND_PHASE_HAND_OVER), show, actorP1))
	})
	t.Run("pre_showdown", func(t *testing.T) {
		assertCode(t, validateShowCards(newTestState(), show, actorP1), CodeCannotShowYet)
	})
}

// ── MuckCards ─────────────────────────────────────────────────────────────────

func TestValidateMuckCards(t *testing.T) {
	muck := &pb.MuckCardsCommand{}

	t.Run("valid_at_showdown", func(t *testing.T) {
		assertNil(t, validateMuckCards(withPhase(newTestState(), pb.HandPhase_HAND_PHASE_SHOWDOWN), muck, actorP1))
	})
	t.Run("valid_at_hand_over", func(t *testing.T) {
		assertNil(t, validateMuckCards(withPhase(newTestState(), pb.HandPhase_HAND_PHASE_HAND_OVER), muck, actorP1))
	})
	t.Run("pre_showdown", func(t *testing.T) {
		assertCode(t, validateMuckCards(newTestState(), muck, actorP1), CodeCannotMuckYet)
	})
}

// ── RunItTwice ────────────────────────────────────────────────────────────────

func TestValidateRunItTwice(t *testing.T) {
	rit := &pb.RunItTwiceCommand{}

	allInTurnState := func() *pb.GameState {
		s := withPhase(newTestState(), pb.HandPhase_HAND_PHASE_TURN)
		return withPlayerAllIn(s, actorP1)
	}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateRunItTwice(allInTurnState(), rit, actorP1))
	})
	t.Run("feature_disabled", func(t *testing.T) {
		s := cloneState(allInTurnState())
		s.Config.AllowRunItTwice = false
		assertCode(t, validateRunItTwice(s, rit, actorP1), CodeCannotRunItTwice)
	})
	t.Run("not_all_in", func(t *testing.T) {
		s := withPhase(newTestState(), pb.HandPhase_HAND_PHASE_TURN)
		assertCode(t, validateRunItTwice(s, rit, actorP1), CodeCannotRunItTwice)
	})
	t.Run("on_river", func(t *testing.T) {
		s := withPhase(withPlayerAllIn(newTestState(), actorP1), pb.HandPhase_HAND_PHASE_RIVER)
		assertCode(t, validateRunItTwice(s, rit, actorP1), CodeCannotRunItTwice)
	})
}

// ── SitOut / SitIn ────────────────────────────────────────────────────────────

func TestValidateSitOut(t *testing.T) {
	sitout := &pb.SitOutCommand{}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateSitOut(newTestState(), sitout, actorP1))
	})
	t.Run("already_sitting_out", func(t *testing.T) {
		assertCode(t, validateSitOut(withPlayerSittingOut(newTestState(), actorP1), sitout, actorP1), CodeAlreadySittingOut)
	})
}

func TestValidateSitIn(t *testing.T) {
	sitin := &pb.SitInCommand{}

	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateSitIn(withPlayerSittingOut(newTestState(), actorP1), sitin, actorP1))
	})
	t.Run("already_sitting_in", func(t *testing.T) {
		assertCode(t, validateSitIn(newTestState(), sitin, actorP1), CodeAlreadySittingIn)
	})
}

// ── UseTimeBank ───────────────────────────────────────────────────────────────

func TestValidateUseTimeBank(t *testing.T) {
	cmd := &pb.UseTimeBankCommand{}

	withTimeBank := func(s *pb.GameState, secs int64) *pb.GameState {
		c := cloneState(s)
		p := *c.Players[actorP1]
		p.TimeBankRemaining = durationpb.New(time.Duration(secs) * time.Second)
		c.Players[actorP1] = &p
		return c
	}

	t.Run("valid", func(t *testing.T) {
		s := withTimeBank(newTestState(), 30)
		assertNil(t, validateUseTimeBank(s, cmd, actorP1))
	})
	t.Run("not_your_turn", func(t *testing.T) {
		s := withTimeBank(withActor(newTestState(), actorP2), 30)
		assertCode(t, validateUseTimeBank(s, cmd, actorP1), CodeTimeBankNotActive)
	})
	t.Run("no_time_bank", func(t *testing.T) {
		assertCode(t, validateUseTimeBank(newTestState(), cmd, actorP1), CodeNoTimeBank)
	})
}

// ── ChatMessage ───────────────────────────────────────────────────────────────

func TestValidateChatMessage(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		cmd := &pb.ChatMessageCommand{Text: "hello"}
		assertNil(t, validateChatMessage(newTestState(), cmd, actorP1))
	})
	t.Run("chat_disabled", func(t *testing.T) {
		s := cloneState(newTestState())
		s.Config.AllowChat = false
		assertCode(t, validateChatMessage(s, &pb.ChatMessageCommand{Text: "hi"}, actorP1), CodeChatDisabled)
	})
	t.Run("empty_text", func(t *testing.T) {
		assertCode(t, validateChatMessage(newTestState(), &pb.ChatMessageCommand{Text: ""}, actorP1), CodeChatMessageEmpty)
	})
	t.Run("whitespace_only", func(t *testing.T) {
		assertCode(t, validateChatMessage(newTestState(), &pb.ChatMessageCommand{Text: "   "}, actorP1), CodeChatMessageEmpty)
	})
	t.Run("too_long", func(t *testing.T) {
		long := strings.Repeat("a", maxChatMessageRunes+1)
		assertCode(t, validateChatMessage(newTestState(), &pb.ChatMessageCommand{Text: long}, actorP1), CodeChatMessageTooLong)
	})
}

// ── Emote ─────────────────────────────────────────────────────────────────────

func TestValidateEmote(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		assertNil(t, validateEmote(newTestState(), &pb.EmoteCommand{EmoteId: "thumbs_up"}, actorP1))
	})
	t.Run("chat_disabled", func(t *testing.T) {
		s := cloneState(newTestState())
		s.Config.AllowChat = false
		assertCode(t, validateEmote(s, &pb.EmoteCommand{EmoteId: "thumbs_up"}, actorP1), CodeChatDisabled)
	})
	t.Run("empty_emote_id", func(t *testing.T) {
		assertCode(t, validateEmote(newTestState(), &pb.EmoteCommand{EmoteId: ""}, actorP1), CodeMalformedCommand)
	})
}

// ── Dispatch ──────────────────────────────────────────────────────────────────

func TestValidateDispatch(t *testing.T) {
	t.Run("nil_payload", func(t *testing.T) {
		cmd := &pb.PlayerCommand{} // Payload is nil
		assertCode(t, validate(newTestState(), cmd, actorP1), CodeMalformedCommand)
	})
}