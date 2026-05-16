package engine

import (
	"fmt"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// handleCommand validates a command against current state and returns the events
// to be persisted. Invalid commands produce a CommandRejected event. Valid commands
// that mutate state will return the real event sequence once the engine is built —
// for now they still produce UNIMPLEMENTED rejections.
//
// JoinTableCommand and LeaveTableCommand are intentionally not handled here —
// they require account-service coordination and live in a separate flow.
func handleCommand(state *pb.GameState, cmd *pb.PlayerCommand, actor string) []*pb.GameEvent {
	if reason := validate(state, cmd, actor); reason != nil {
		return []*pb.GameEvent{reason.toCommandRejectedEvent(state.GetGameId(), cmd.GetCommandId())}
	}
	return []*pb.GameEvent{
		(&RejectionReason{
			Code:   "UNIMPLEMENTED",
			Reason: fmt.Sprintf("command %T validated but engine not implemented", cmd.Payload),
		}).toCommandRejectedEvent(state.GetGameId(), cmd.GetCommandId()),
	}
}

// validate dispatches to the per-command validator.
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
	case *pb.PlayerCommand_Resume:
		return validateResume(state, p.Resume, actor)
	default:
		return reject(CodeUnknownCommand, "unknown command type %T", cmd.Payload)
	}
}