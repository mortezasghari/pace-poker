package server

import (
	"io"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// runSession drives the bidi-streaming PlaySession loop.
// It reads PlayerCommands from the client and sends back GameEvents.
// Every command variant returns a CommandRejected until real game logic is wired in.
func runSession(stream pb.PokerService_PlaySessionServer) error {
	for {
		cmd, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		evt, err := handleCommand(cmd)
		if err != nil {
			return err
		}

		if sendErr := stream.Send(evt); sendErr != nil {
			return sendErr
		}
	}
}

func handleCommand(cmd *pb.PlayerCommand) (*pb.GameEvent, error) {
	code, reason := dispatchCommand(cmd)

	return &pb.GameEvent{
		GameId:            cmd.GetGameId(),
		OccurredAt:        timestamppb.Now(),
		CausedByCommandId: cmd.GetCommandId(),
		Event: &pb.GameEvent_CommandRejected{
			CommandRejected: &pb.CommandRejected{
				CommandId: cmd.GetCommandId(),
				Reason:    reason,
				Code:      code,
			},
		},
	}, nil
}

// dispatchCommand returns the (code, reason) for the CommandRejected event.
func dispatchCommand(cmd *pb.PlayerCommand) (code, reason string) {
	switch cmd.Payload.(type) {
	case *pb.PlayerCommand_Fold:
		return "UNIMPLEMENTED", "fold not implemented"
	case *pb.PlayerCommand_Check:
		return "UNIMPLEMENTED", "check not implemented"
	case *pb.PlayerCommand_Call:
		return "UNIMPLEMENTED", "call not implemented"
	case *pb.PlayerCommand_Bet:
		return "UNIMPLEMENTED", "bet not implemented"
	case *pb.PlayerCommand_Raise:
		return "UNIMPLEMENTED", "raise not implemented"
	case *pb.PlayerCommand_AllIn:
		return "UNIMPLEMENTED", "all-in not implemented"
	case *pb.PlayerCommand_SitOut:
		return "UNIMPLEMENTED", "sit-out not implemented"
	case *pb.PlayerCommand_SitIn:
		return "UNIMPLEMENTED", "sit-in not implemented"
	case *pb.PlayerCommand_ShowCards:
		return "UNIMPLEMENTED", "show-cards not implemented"
	case *pb.PlayerCommand_MuckCards:
		return "UNIMPLEMENTED", "muck-cards not implemented"
	case *pb.PlayerCommand_UseTimeBank:
		return "UNIMPLEMENTED", "use-time-bank not implemented"
	case *pb.PlayerCommand_ChatMessage:
		return "UNIMPLEMENTED", "chat-message not implemented"
	case *pb.PlayerCommand_Emote:
		return "UNIMPLEMENTED", "emote not implemented"
	case *pb.PlayerCommand_RunItTwice:
		return "UNIMPLEMENTED", "run-it-twice not implemented"
	case *pb.PlayerCommand_Resume:
		return "RESUME_NOT_IMPLEMENTED", "resume not implemented"
	default:
		return "UNKNOWN_COMMAND", "unrecognized command payload"
	}
}

// handleCommandErr is a helper for callers that need to surface a gRPC status
// error from inside the session loop (e.g. auth or validation failures before dispatch).
func handleCommandErr(code codes.Code, msg string) error {
	return status.Error(code, msg)
}