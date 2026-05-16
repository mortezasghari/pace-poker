package engine

import pb "github.com/pacepoker/poker/gen/go/poker/v1"

// requireActiveGame checks that the table is open for play.
// Returns nil for ACTIVE and PAUSED — per-command validators decide whether
// their specific command is legal when paused.
func requireActiveGame(state *pb.GameState) *RejectionReason {
	switch state.Status {
	case pb.GameStatus_GAME_STATUS_CLOSED:
		return reject(CodeTableClosed, "table is closed")
	case pb.GameStatus_GAME_STATUS_ACTIVE,
		pb.GameStatus_GAME_STATUS_PAUSED:
		return nil
	default:
		return reject(CodeGameNotActive, "game is not currently active")
	}
}

// requireHandInProgress checks that a hand is actively being played.
func requireHandInProgress(state *pb.GameState) *RejectionReason {
	if state.CurrentHand == nil {
		return reject(CodeHandNotInProgress, "no hand in progress")
	}
	phase := state.CurrentHand.Phase
	if phase == pb.HandPhase_HAND_PHASE_HAND_OVER ||
		phase == pb.HandPhase_HAND_PHASE_UNSPECIFIED {
		return reject(CodeHandNotInProgress, "hand is not in a playable phase")
	}
	return nil
}

// requireSeated returns the actor's PlayerState if they're seated, or a rejection.
func requireSeated(state *pb.GameState, actorPlayerID string) (*pb.PlayerState, *RejectionReason) {
	p, ok := state.Players[actorPlayerID]
	if !ok {
		return nil, reject(CodeNotAtTable, "player %s is not at this table", actorPlayerID)
	}
	return p, nil
}

// requireActorsTurn checks that it's actorPlayerID's turn to act.
// Caller must have already called requireHandInProgress.
func requireActorsTurn(state *pb.GameState, actorPlayerID string) *RejectionReason {
	h := state.CurrentHand
	if h.ActingPlayerId == nil || *h.ActingPlayerId == "" {
		return reject(CodeNotYourTurn, "no player is currently acting")
	}
	if *h.ActingPlayerId != actorPlayerID {
		return reject(CodeNotYourTurn, "it is not your turn to act")
	}
	return nil
}

// requireCanAct checks that the player's state allows making an action
// (not folded, not all-in, not sitting out).
func requireCanAct(p *pb.PlayerState) *RejectionReason {
	if p.IsSittingOut {
		return reject(CodePlayerSittingOut, "player is sitting out")
	}
	if p.IsFolded {
		return reject(CodePlayerFolded, "player has folded this hand")
	}
	if p.IsAllIn {
		return reject(CodePlayerAllIn, "player is all-in")
	}
	return nil
}

// callAmount returns how much the actor must put in to call.
// Returns 0 if there is nothing to call.
func callAmount(h *pb.HandState, p *pb.PlayerState) int64 {
	if h.CurrentHighestBet <= p.CurrentBet {
		return 0
	}
	return h.CurrentHighestBet - p.CurrentBet
}

// minRaiseTo returns the minimum total bet (the "to" amount) for a legal raise.
// In No-Limit, a raise must be at least the size of the previous raise (or the BB
// if no prior raise this street).
func minRaiseTo(state *pb.GameState, h *pb.HandState) int64 {
	minRaiseBy := h.LastRaiseAmount
	if state.Config != nil && minRaiseBy < state.Config.BigBlind {
		minRaiseBy = state.Config.BigBlind
	}
	return h.CurrentHighestBet + minRaiseBy
}