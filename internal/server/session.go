package server

import (
	"io"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// PlaySession drives the bidi-streaming PlaySession RPC.
// The actor identity is extracted once at stream open and reused for every
// command — a long-lived stream always acts as the same authenticated user.
func (s *Server) PlaySession(stream pb.PokerService_PlaySessionServer) error {
	ctx := stream.Context()
	actorID, err := actorFromContext(ctx)
	if err != nil {
		return err
	}
	actorStr := actorID.String()

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

		events, err := s.router.Submit(ctx, gameID, actorStr, cmd)
		if err != nil {
			if sendErr := stream.Send(buildRejection(cmd, "INTERNAL", err.Error())); sendErr != nil {
				return sendErr
			}
			continue
		}

		for _, ev := range events {
			if shouldSuppressFromActor(ev, actorStr) {
				continue
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// shouldSuppressFromActor returns true for HoleCardsRevealed events that belong
// to a different player — the acting player must not see opponents' hole cards.
func shouldSuppressFromActor(ev *pb.GameEvent, actorPlayerID string) bool {
	r, ok := ev.Event.(*pb.GameEvent_HoleCardsRevealed)
	if !ok {
		return false
	}
	return r.HoleCardsRevealed.GetPlayerId() != actorPlayerID
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