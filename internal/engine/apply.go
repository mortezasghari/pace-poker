package engine

import pb "github.com/pacepoker/poker/gen/go/poker/v1"

// applyEvents folds a list of events into a new GameState.
// Must be deterministic: replaying the same events on the same starting state
// always produces the same result.
//
// Stub: returns state unchanged. Real implementation comes when the engine is built.
func applyEvents(state *pb.GameState, events []*pb.GameEvent) *pb.GameState {
	// TODO: implement the reducer for each event type.
	_ = events
	return state
}