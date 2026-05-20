package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LoadState reconstructs the current GameState by replaying the latest
// snapshot plus all subsequent events. It is the same logic as the router's
// private loadState but exported so the lobby store layer can use it inside
// a transaction without going through the router.
func LoadState(ctx context.Context, st store.Store, gameID uuid.UUID) (*pb.GameState, uint64, error) {
	state, snapSeq, err := st.GetLatestSnapshot(ctx, gameID)
	if err != nil {
		return nil, 0, fmt.Errorf("load snapshot for %s: %w", gameID, err)
	}

	latestSeq := snapSeq
	for {
		events, err := st.GetEventsForGame(ctx, gameID, latestSeq+1, 500)
		if err != nil {
			return nil, 0, fmt.Errorf("load events for %s after seq %d: %w", gameID, latestSeq, err)
		}
		if len(events) == 0 {
			break
		}
		state = applyAll(state, events)
		latestSeq = events[len(events)-1].GetSequence()
	}
	return state, latestSeq, nil
}

// BuildPlayerJoinedEvent constructs a stamped PlayerJoined event.
// playerID and userID may be the same (lobby join uses userID for both).
func BuildPlayerJoinedEvent(gameID uuid.UUID, seq uint64, cmdID uuid.UUID, playerID, userID string, seat int32, buyIn, stateVersion int64) *pb.GameEvent {
	var cmdStr string
	if cmdID != uuid.Nil {
		cmdStr = cmdID.String()
	}
	return &pb.GameEvent{
		GameId:            gameID.String(),
		Sequence:          seq,
		StateVersion:      stateVersion,
		OccurredAt:        timestamppb.Now(),
		CausedByCommandId: cmdStr,
		Event: &pb.GameEvent_PlayerJoined{
			PlayerJoined: &pb.PlayerJoined{
				PlayerId: playerID,
				UserId:   userID,
				Seat:     seat,
				BuyIn:    buyIn,
			},
		},
	}
}

// BuildPlayerLeftEvent constructs a stamped PlayerLeft event.
func BuildPlayerLeftEvent(gameID uuid.UUID, seq uint64, cmdID uuid.UUID, playerID string, cashOut, stateVersion int64) *pb.GameEvent {
	var cmdStr string
	if cmdID != uuid.Nil {
		cmdStr = cmdID.String()
	}
	return &pb.GameEvent{
		GameId:            gameID.String(),
		Sequence:          seq,
		StateVersion:      stateVersion,
		OccurredAt:        timestamppb.Now(),
		CausedByCommandId: cmdStr,
		Event: &pb.GameEvent_PlayerLeft{
			PlayerLeft: &pb.PlayerLeft{
				PlayerId:      playerID,
				CashOutAmount: cashOut,
				Reason:        "leave_request",
			},
		},
	}
}

// BuildAutoFoldEvent constructs a stamped PlayerFolded event for an auto-fold
// triggered when a player leaves during an active hand.
func BuildAutoFoldEvent(gameID uuid.UUID, seq uint64, cmdID uuid.UUID, playerID string, stateVersion int64) *pb.GameEvent {
	var cmdStr string
	if cmdID != uuid.Nil {
		cmdStr = cmdID.String()
	}
	return &pb.GameEvent{
		GameId:            gameID.String(),
		Sequence:          seq,
		StateVersion:      stateVersion,
		OccurredAt:        timestamppb.Now(),
		CausedByCommandId: cmdStr,
		Event: &pb.GameEvent_PlayerFolded{
			PlayerFolded: &pb.PlayerFolded{
				PlayerId: playerID,
			},
		},
	}
}
