package engine

import pb "github.com/pacepoker/poker/gen/go/poker/v1"

func validateShowCards(state *pb.GameState, _ *pb.ShowCardsCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if state.CurrentHand == nil {
		return reject(CodeHandNotInProgress, "no hand in progress")
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if p.IsSittingOut {
		return reject(CodePlayerSittingOut, "sitting out")
	}
	phase := state.CurrentHand.Phase
	if phase != pb.HandPhase_HAND_PHASE_SHOWDOWN &&
		phase != pb.HandPhase_HAND_PHASE_HAND_OVER {
		return reject(CodeCannotShowYet, "can only show cards at showdown")
	}
	return nil
}

func validateMuckCards(state *pb.GameState, _ *pb.MuckCardsCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if state.CurrentHand == nil {
		return reject(CodeHandNotInProgress, "no hand in progress")
	}
	if _, r := requireSeated(state, actor); r != nil {
		return r
	}
	phase := state.CurrentHand.Phase
	if phase != pb.HandPhase_HAND_PHASE_SHOWDOWN &&
		phase != pb.HandPhase_HAND_PHASE_HAND_OVER {
		return reject(CodeCannotMuckYet, "can only muck cards at showdown")
	}
	return nil
}

func validateRunItTwice(state *pb.GameState, _ *pb.RunItTwiceCommand, actor string) *RejectionReason {
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
	if state.Config == nil || !state.Config.AllowRunItTwice {
		return reject(CodeCannotRunItTwice, "run-it-twice is disabled at this table")
	}
	if !p.IsAllIn {
		return reject(CodeCannotRunItTwice, "you must be all-in to run it twice")
	}
	phase := state.CurrentHand.Phase
	if phase == pb.HandPhase_HAND_PHASE_RIVER ||
		phase == pb.HandPhase_HAND_PHASE_SHOWDOWN ||
		phase == pb.HandPhase_HAND_PHASE_HAND_OVER {
		return reject(CodeCannotRunItTwice, "too late to run it twice in phase %s", phase)
	}
	return nil
}