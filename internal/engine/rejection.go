package engine

import (
	"fmt"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// RejectionCode is a stable identifier for why a command was rejected.
// Clients localize on the code; logs and metrics group on the code.
// Never change a code value — only add new ones.
type RejectionCode string

const (
	// Identity / authorization
	CodeNotAtTable       RejectionCode = "NOT_AT_TABLE"
	CodeNotYourTurn      RejectionCode = "NOT_YOUR_TURN"
	CodePlayerSittingOut RejectionCode = "PLAYER_SITTING_OUT"
	CodePlayerFolded     RejectionCode = "PLAYER_FOLDED"
	CodePlayerAllIn      RejectionCode = "PLAYER_ALL_IN"

	// Game phase
	CodeWrongPhase        RejectionCode = "WRONG_PHASE"
	CodeHandNotInProgress RejectionCode = "HAND_NOT_IN_PROGRESS"
	CodeGameNotActive     RejectionCode = "GAME_NOT_ACTIVE"
	CodeTableClosed       RejectionCode = "TABLE_CLOSED"

	// Action legality
	CodeIllegalAction       RejectionCode = "ILLEGAL_ACTION"
	CodeCannotCheck         RejectionCode = "CANNOT_CHECK"
	CodeCannotFold          RejectionCode = "CANNOT_FOLD"
	CodeCannotShowYet       RejectionCode = "CANNOT_SHOW_YET"
	CodeCannotMuckYet       RejectionCode = "CANNOT_MUCK_YET"
	CodeCannotRunItTwice    RejectionCode = "CANNOT_RUN_IT_TWICE"
	// A sub-minimum all-in raise did not reopen this player's raise option.
	CodeRaiseActionClosed RejectionCode = "RAISE_ACTION_CLOSED"

	// Amount-based
	CodeIllegalAmount       RejectionCode = "ILLEGAL_AMOUNT"
	CodeAmountBelowMinBet   RejectionCode = "AMOUNT_BELOW_MIN_BET"
	CodeAmountBelowMinRaise RejectionCode = "AMOUNT_BELOW_MIN_RAISE"
	CodeAmountAboveStack    RejectionCode = "AMOUNT_ABOVE_STACK"
	CodeRaiseCapReached     RejectionCode = "RAISE_CAP_REACHED"
	CodeCallAmountMismatch  RejectionCode = "CALL_AMOUNT_MISMATCH"

	// Time bank
	CodeNoTimeBank        RejectionCode = "NO_TIME_BANK"
	CodeTimeBankNotActive RejectionCode = "TIME_BANK_NOT_ACTIVE"

	// Sit in/out
	CodeAlreadySittingOut RejectionCode = "ALREADY_SITTING_OUT"
	CodeAlreadySittingIn  RejectionCode = "ALREADY_SITTING_IN"

	// Chat
	CodeChatDisabled      RejectionCode = "CHAT_DISABLED"
	CodeChatMuted         RejectionCode = "CHAT_MUTED"
	CodeChatMessageEmpty  RejectionCode = "CHAT_MESSAGE_EMPTY"
	CodeChatMessageTooLong RejectionCode = "CHAT_MESSAGE_TOO_LONG"

	// Generic
	CodeMalformedCommand RejectionCode = "MALFORMED_COMMAND"
	CodeUnknownCommand   RejectionCode = "UNKNOWN_COMMAND"
)

// RejectionReason describes why a command failed validation.
type RejectionReason struct {
	Code   RejectionCode
	Reason string
}

func reject(code RejectionCode, format string, args ...any) *RejectionReason {
	return &RejectionReason{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// toCommandRejectedEvent converts a RejectionReason into a CommandRejected event.
func (r *RejectionReason) toCommandRejectedEvent(gameID, commandID string) *pb.GameEvent {
	return &pb.GameEvent{
		GameId: gameID,
		Event: &pb.GameEvent_CommandRejected{
			CommandRejected: &pb.CommandRejected{
				CommandId: commandID,
				Code:      string(r.Code),
				Reason:    r.Reason,
			},
		},
	}
}