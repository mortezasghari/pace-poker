package engine

import pb "github.com/pacepoker/poker/gen/go/poker/v1"

func validateFold(state *pb.GameState, _ *pb.FoldCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireCanAct(p); r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return r
	}
	return nil
}

func validateCheck(state *pb.GameState, _ *pb.CheckCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireCanAct(p); r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return r
	}
	if callAmount(state.CurrentHand, p) > 0 {
		return reject(CodeCannotCheck, "there is a bet of %d to call", state.CurrentHand.CurrentHighestBet)
	}
	return nil
}

func validateCall(state *pb.GameState, cmd *pb.CallCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireCanAct(p); r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return r
	}
	needed := callAmount(state.CurrentHand, p)
	if needed == 0 {
		return reject(CodeIllegalAction, "nothing to call; use check instead")
	}
	if cmd.Amount > 0 && cmd.Amount != needed {
		return reject(CodeCallAmountMismatch,
			"call amount %d does not match required %d", cmd.Amount, needed)
	}
	return nil
}

func validateBet(state *pb.GameState, cmd *pb.BetCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireCanAct(p); r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return r
	}
	if state.CurrentHand.CurrentHighestBet > 0 {
		return reject(CodeIllegalAction, "use raise; there is already a bet of %d",
			state.CurrentHand.CurrentHighestBet)
	}
	if cmd.Amount <= 0 {
		return reject(CodeIllegalAmount, "bet amount must be positive")
	}
	bb := int64(0)
	if state.Config != nil {
		bb = state.Config.BigBlind
	}
	if cmd.Amount < bb && cmd.Amount != p.Stack {
		return reject(CodeAmountBelowMinBet,
			"bet must be at least %d (big blind) unless going all-in", bb)
	}
	if cmd.Amount > p.Stack {
		return reject(CodeAmountAboveStack, "bet %d exceeds stack %d", cmd.Amount, p.Stack)
	}
	return nil
}

func validateRaise(state *pb.GameState, cmd *pb.RaiseCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireCanAct(p); r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return r
	}
	h := state.CurrentHand
	if h.CurrentHighestBet == 0 {
		return reject(CodeIllegalAction, "no bet to raise; use bet instead")
	}
	// A sub-minimum all-in raise does not reopen the raise option for players
	// who had already acted against the previous full bet.
	for _, pid := range h.ActionClosedFor {
		if pid == actor {
			return reject(CodeRaiseActionClosed,
				"a sub-minimum all-in raise did not reopen your action; you may call or fold")
		}
	}
	if cmd.To <= h.CurrentHighestBet {
		return reject(CodeIllegalAmount, "raise to %d must exceed current bet %d", cmd.To, h.CurrentHighestBet)
	}
	needed := cmd.To - p.CurrentBet
	if needed > p.Stack {
		return reject(CodeAmountAboveStack, "raise to %d requires %d but stack is %d",
			cmd.To, needed, p.Stack)
	}
	minTo := minRaiseTo(state, h)
	if cmd.To < minTo && needed != p.Stack {
		return reject(CodeAmountBelowMinRaise,
			"raise to %d is below minimum %d (unless going all-in)", cmd.To, minTo)
	}
	if state.Config != nil &&
		state.Config.Structure == pb.BettingStructure_BETTING_STRUCTURE_FIXED_LIMIT {
		cap := state.Config.MaxRaisesPerStreet
		if state.Config.UncapHeadsUp && playersInHand(state) == 2 {
			cap = 0
		}
		if cap > 0 && h.RaisesThisStreet >= cap {
			return reject(CodeRaiseCapReached,
				"raise cap of %d reached for this street", cap)
		}
	}
	return nil
}

func validateAllIn(state *pb.GameState, _ *pb.AllInCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireCanAct(p); r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return r
	}
	if p.Stack <= 0 {
		return reject(CodeIllegalAction, "stack is zero")
	}
	return nil
}

// playersInHand counts players who are still active (seated, not folded, not sitting out).
func playersInHand(state *pb.GameState) int {
	n := 0
	for _, p := range state.Players {
		if !p.IsFolded && !p.IsSittingOut {
			n++
		}
	}
	return n
}