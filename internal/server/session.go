package server

import (
	"io"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// PlaySession drives the bidi-streaming PlaySession RPC.
// Commands are routed through the engine.Router; events are streamed back.
func (s *Server) PlaySession(stream pb.PokerService_PlaySessionServer) error {
	ctx := stream.Context()
	for {
		cmd, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		gameID, err := uuid.Parse(cmd.GetGameId())
		if err != nil {
			if sendErr := stream.Send(buildRejection(cmd, "INVALID_GAME_ID", err.Error())); sendErr != nil {
				return sendErr
			}
			continue
		}

		events, err := s.router.Submit(ctx, gameID, cmd)
		if err != nil {
			if sendErr := stream.Send(buildRejection(cmd, "INTERNAL", err.Error())); sendErr != nil {
				return sendErr
			}
			continue
		}

		for _, ev := range events {
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

func buildRejection(cmd *pb.PlayerCommand, code, reason string) *pb.GameEvent {
	return &pb.GameEvent{
		GameId: cmd.GetGameId(),
		Event: &pb.GameEvent_CommandRejected{
			CommandRejected: &pb.CommandRejected{
				CommandId: cmd.GetCommandId(),
				Code:      code,
				Reason:    reason,
			},
		},
	}
}