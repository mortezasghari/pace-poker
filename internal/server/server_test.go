package server

import (
	"testing"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// TestFoldCommandRejected verifies that a FoldCommand sent through the session
// handler returns exactly one CommandRejected event with code "UNIMPLEMENTED".
func TestFoldCommandRejected(t *testing.T) {
	cmd := &pb.PlayerCommand{
		CommandId: "test-cmd-id",
		GameId:    "test-game-id",
		Payload:   &pb.PlayerCommand_Fold{Fold: &pb.FoldCommand{}},
	}

	evt, err := handleCommand(cmd)
	if err != nil {
		t.Fatalf("handleCommand returned unexpected error: %v", err)
	}

	rejected, ok := evt.Event.(*pb.GameEvent_CommandRejected)
	if !ok {
		t.Fatalf("expected CommandRejected event, got %T", evt.Event)
	}

	if got := rejected.CommandRejected.GetCode(); got != "UNIMPLEMENTED" {
		t.Errorf("expected code UNIMPLEMENTED, got %q", got)
	}

	if got := rejected.CommandRejected.GetCommandId(); got != cmd.GetCommandId() {
		t.Errorf("expected command_id %q, got %q", cmd.GetCommandId(), got)
	}

	if got := evt.GetCausedByCommandId(); got != cmd.GetCommandId() {
		t.Errorf("expected caused_by_command_id %q, got %q", cmd.GetCommandId(), got)
	}
}