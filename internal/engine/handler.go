package engine

import (
	"fmt"
	"time"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// HandleCommand is the top-level entry point for all player commands.
// It validates the command, builds the direct command event(s), then
// drives the lifecycle forward via advance until the engine is quiescent
// (waiting for the next player action or for more players to join).
//
// The returned slice contains all events that must be persisted and broadcast,
// in order. The caller (Session.process) is responsible for persistence.
func HandleCommand(state *pb.GameState, cmd *pb.PlayerCommand, actor string, dlr Dealer) ([]*pb.GameEvent, error) {
	// 1. Validate and produce the direct command event(s).
	cmdEvents, err := commandEvents(state, cmd, actor)
	if err != nil {
		return nil, err
	}

	// 2. Apply command events to get updated state.
	curr := applyAll(state, cmdEvents)

	// 3. Advance the lifecycle until quiescent.
	advanceEvents, err := runAdvance(curr, dlr)
	if err != nil {
		return nil, fmt.Errorf("advance: %w", err)
	}

	return append(cmdEvents, advanceEvents...), nil
}

// runAdvance loops advance → applyAll until advance returns nil.
// Guards against infinite loops with a step cap.
func runAdvance(state *pb.GameState, dlr Dealer) ([]*pb.GameEvent, error) {
	const maxSteps = 64
	var all []*pb.GameEvent
	now := time.Now()
	for range maxSteps {
		batch, err := advance(state, dlr, now)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		state = applyAll(state, batch)
		all = append(all, batch...)
	}
	return all, nil
}

// commandEvents validates the command and returns its direct event(s).
// Rejected commands produce a single CommandRejected event (not a Go error).
func commandEvents(state *pb.GameState, cmd *pb.PlayerCommand, actor string) ([]*pb.GameEvent, error) {
	if reason := validate(state, cmd, actor); reason != nil {
		return []*pb.GameEvent{reason.toCommandRejectedEvent(state.GetGameId(), cmd.GetCommandId())}, nil
	}
	evts := buildCommandEvents(state, cmd, actor)
	return evts, nil
}

// buildCommandEvents maps a validated command to its event(s).
// Every case here must assume the command passed validate().
func buildCommandEvents(state *pb.GameState, cmd *pb.PlayerCommand, actor string) []*pb.GameEvent {
	gameID := state.GetGameId()
	cmdID := cmd.GetCommandId()

	switch p := cmd.Payload.(type) {
	case *pb.PlayerCommand_Fold:
		_ = p
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerFolded{PlayerFolded: &pb.PlayerFolded{PlayerId: actor}}}}

	case *pb.PlayerCommand_Check:
		_ = p
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerChecked{PlayerChecked: &pb.PlayerChecked{PlayerId: actor}}}}

	case *pb.PlayerCommand_Call:
		h := state.CurrentHand
		player := state.Players[actor]
		amount := callAmount(h, player)
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerCalled{PlayerCalled: &pb.PlayerCalled{
			PlayerId: actor,
			Amount:   amount,
		}}}}

	case *pb.PlayerCommand_Bet:
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerBet{PlayerBet: &pb.PlayerBet{
			PlayerId: actor,
			Amount:   p.Bet.GetAmount(),
		}}}}

	case *pb.PlayerCommand_Raise:
		h := state.CurrentHand
		raiseBy := p.Raise.GetTo() - h.CurrentHighestBet
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerRaised{PlayerRaised: &pb.PlayerRaised{
			PlayerId: actor,
			To:       p.Raise.GetTo(),
			RaiseBy:  raiseBy,
		}}}}

	case *pb.PlayerCommand_AllIn:
		player := state.Players[actor]
		total := player.CurrentBet + player.Stack
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerAllIn{PlayerAllIn: &pb.PlayerAllIn{
			PlayerId: actor,
			Amount:   total,
		}}}}

	case *pb.PlayerCommand_SitOut:
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerSatOut{PlayerSatOut: &pb.PlayerSatOut{PlayerId: actor}}}}

	case *pb.PlayerCommand_SitIn:
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerSatIn{PlayerSatIn: &pb.PlayerSatIn{PlayerId: actor}}}}

	case *pb.PlayerCommand_ShowCards:
		player := state.Players[actor]
		var c1, c2 uint32
		if player.HoleCard_1 != nil {
			c1 = *player.HoleCard_1
		}
		if player.HoleCard_2 != nil {
			c2 = *player.HoleCard_2
		}
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerShowedCards{PlayerShowedCards: &pb.PlayerShowedCards{
			PlayerId: actor,
			Card_1:   c1,
			Card_2:   c2,
		}}}}

	case *pb.PlayerCommand_MuckCards:
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerMuckedCards{PlayerMuckedCards: &pb.PlayerMuckedCards{PlayerId: actor}}}}

	case *pb.PlayerCommand_UseTimeBank:
		_ = p
		// UseTimeBank is informational; timing enforcement is external.
		return []*pb.GameEvent{{Event: &pb.GameEvent_CommandAccepted{CommandAccepted: &pb.CommandAccepted{
			CommandId: cmdID,
		}}}}

	case *pb.PlayerCommand_ChatMessage:
		return []*pb.GameEvent{{Event: &pb.GameEvent_ChatMessageSent{ChatMessageSent: &pb.ChatMessageSent{
			PlayerId: actor,
			Text:     p.ChatMessage.GetText(),
		}}}}

	case *pb.PlayerCommand_Emote:
		return []*pb.GameEvent{{Event: &pb.GameEvent_PlayerEmoteSent{PlayerEmoteSent: &pb.PlayerEmoteSent{
			PlayerId: actor,
			EmoteId:  p.Emote.GetEmoteId(),
		}}}}

	case *pb.PlayerCommand_RunItTwice:
		_ = p
		return []*pb.GameEvent{{Event: &pb.GameEvent_RunItTwiceAgreed{RunItTwiceAgreed: &pb.RunItTwiceAgreed{PlayerIds: []string{actor}}}}}

	default:
		return []*pb.GameEvent{
			(&RejectionReason{Code: CodeUnknownCommand, Reason: fmt.Sprintf("unhandled command type %T", cmd.Payload)}).
				toCommandRejectedEvent(gameID, cmdID),
		}
	}
}