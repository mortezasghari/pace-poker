package engine

import pb "github.com/pacepoker/poker/gen/go/poker/v1"

// handleCommand is the rules engine (stub).
// Pure function: no I/O, no state mutation. Returns CommandRejected for every
// command until real game logic is wired in.
func handleCommand(state *pb.GameState, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	rejection := &pb.CommandRejected{
		CommandId: cmd.GetCommandId(),
		Reason:    "not implemented",
		Code:      "UNIMPLEMENTED",
	}

	switch cmd.Payload.(type) {
	case *pb.PlayerCommand_Fold,
		*pb.PlayerCommand_Check,
		*pb.PlayerCommand_Call,
		*pb.PlayerCommand_Bet,
		*pb.PlayerCommand_Raise,
		*pb.PlayerCommand_AllIn,
		*pb.PlayerCommand_SitOut,
		*pb.PlayerCommand_SitIn,
		*pb.PlayerCommand_ShowCards,
		*pb.PlayerCommand_MuckCards,
		*pb.PlayerCommand_UseTimeBank,
		*pb.PlayerCommand_ChatMessage,
		*pb.PlayerCommand_Emote,
		*pb.PlayerCommand_RunItTwice,
		*pb.PlayerCommand_Resume:
		// All known variants: UNIMPLEMENTED.
	default:
		rejection.Code = "UNKNOWN_COMMAND"
	}

	return []*pb.GameEvent{{
		GameId: state.GetGameId(),
		Event:  &pb.GameEvent_CommandRejected{CommandRejected: rejection},
	}}, nil
}