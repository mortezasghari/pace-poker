package engine

import (
	"strings"
	"unicode/utf8"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

const maxChatMessageRunes = 500

func validateSitOut(state *pb.GameState, _ *pb.SitOutCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if p.IsSittingOut {
		return reject(CodeAlreadySittingOut, "already sitting out")
	}
	return nil
}

func validateSitIn(state *pb.GameState, _ *pb.SitInCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if !p.IsSittingOut {
		return reject(CodeAlreadySittingIn, "already sitting in")
	}
	return nil
}

func validateUseTimeBank(state *pb.GameState, _ *pb.UseTimeBankCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if r := requireHandInProgress(state); r != nil {
		return r
	}
	p, r := requireSeated(state, actor)
	if r != nil {
		return r
	}
	if r := requireActorsTurn(state, actor); r != nil {
		return reject(CodeTimeBankNotActive, "%s", r.Reason)
	}
	if p.TimeBankRemaining == nil || p.TimeBankRemaining.AsDuration() <= 0 {
		return reject(CodeNoTimeBank, "no time bank remaining")
	}
	return nil
}

func validateChatMessage(state *pb.GameState, cmd *pb.ChatMessageCommand, actor string) *RejectionReason {
	if state.Config == nil || !state.Config.AllowChat {
		return reject(CodeChatDisabled, "chat is disabled at this table")
	}
	if _, r := requireSeated(state, actor); r != nil {
		return r
	}
	trimmed := strings.TrimSpace(cmd.Text)
	if trimmed == "" {
		return reject(CodeChatMessageEmpty, "chat message is empty")
	}
	if utf8.RuneCountInString(cmd.Text) > maxChatMessageRunes {
		return reject(CodeChatMessageTooLong, "chat message exceeds %d characters", maxChatMessageRunes)
	}
	return nil
}

func validateEmote(state *pb.GameState, cmd *pb.EmoteCommand, actor string) *RejectionReason {
	if state.Config == nil || !state.Config.AllowChat {
		return reject(CodeChatDisabled, "chat is disabled at this table")
	}
	if _, r := requireSeated(state, actor); r != nil {
		return r
	}
	if strings.TrimSpace(cmd.EmoteId) == "" {
		return reject(CodeMalformedCommand, "emote_id is required")
	}
	return nil
}

func validateResume(state *pb.GameState, _ *pb.ResumeCommand, actor string) *RejectionReason {
	if r := requireActiveGame(state); r != nil {
		return r
	}
	if _, r := requireSeated(state, actor); r != nil {
		return r
	}
	return nil
}