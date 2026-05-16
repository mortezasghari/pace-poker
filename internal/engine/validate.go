package engine

import pb "github.com/pacepoker/poker/gen/go/poker/v1"

// validate dispatches to the per-command validator and returns a rejection
// reason if the command is not allowed in the current state, or nil if valid.
func validate(state *pb.GameState, cmd *pb.PlayerCommand, actor string) *RejectionReason {
	if cmd == nil || cmd.Payload == nil {
		return reject(CodeMalformedCommand, "command payload is empty")
	}
	switch p := cmd.Payload.(type) {
	case *pb.PlayerCommand_Fold:
		return validateFold(state, p.Fold, actor)
	case *pb.PlayerCommand_Check:
		return validateCheck(state, p.Check, actor)
	case *pb.PlayerCommand_Call:
		return validateCall(state, p.Call, actor)
	case *pb.PlayerCommand_Bet:
		return validateBet(state, p.Bet, actor)
	case *pb.PlayerCommand_Raise:
		return validateRaise(state, p.Raise, actor)
	case *pb.PlayerCommand_AllIn:
		return validateAllIn(state, p.AllIn, actor)
	case *pb.PlayerCommand_SitOut:
		return validateSitOut(state, p.SitOut, actor)
	case *pb.PlayerCommand_SitIn:
		return validateSitIn(state, p.SitIn, actor)
	case *pb.PlayerCommand_ShowCards:
		return validateShowCards(state, p.ShowCards, actor)
	case *pb.PlayerCommand_MuckCards:
		return validateMuckCards(state, p.MuckCards, actor)
	case *pb.PlayerCommand_UseTimeBank:
		return validateUseTimeBank(state, p.UseTimeBank, actor)
	case *pb.PlayerCommand_ChatMessage:
		return validateChatMessage(state, p.ChatMessage, actor)
	case *pb.PlayerCommand_Emote:
		return validateEmote(state, p.Emote, actor)
	case *pb.PlayerCommand_RunItTwice:
		return validateRunItTwice(state, p.RunItTwice, actor)
	default:
		return reject(CodeUnknownCommand, "unknown command type %T", cmd.Payload)
	}
}

// requireTableOpen checks that the table is not closed. Allows WAITING, ACTIVE,
// and PAUSED. Use this for commands that should work between hands (sit-out,
// sit-in, chat, emote).
func requireTableOpen(state *pb.GameState) *RejectionReason {
	if state.Status == pb.GameStatus_GAME_STATUS_CLOSED {
		return reject(CodeTableClosed, "table is closed")
	}
	return nil
}

// requireActiveGame checks that the table is open for play (a hand may be
// running or about to run). Returns nil for ACTIVE and PAUSED only.
// Use this for in-hand action commands (fold, check, call, bet, raise, all-in).
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
// Disconnected players are not blocked here: the session layer handles timeouts
// via PlayerTimedOut and a default action; they remain "active" until timed out.
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

// callAmount returns how much the actor must put in to call, capped at their
// remaining stack. A capped call is an all-in; apply sets IsAllIn when Stack==0.
// Returns 0 if there is nothing to call.
func callAmount(h *pb.HandState, p *pb.PlayerState) int64 {
	if h.CurrentHighestBet <= p.CurrentBet {
		return 0
	}
	needed := h.CurrentHighestBet - p.CurrentBet
	if needed > p.Stack {
		return p.Stack
	}
	return needed
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