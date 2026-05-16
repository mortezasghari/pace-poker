package engine

import (
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

func applyEvents(state *pb.GameState, events []*pb.GameEvent) *pb.GameState {
	for _, evt := range events {
		applyEvent(state, evt)
	}
	return state
}

func applyEvent(state *pb.GameState, evt *pb.GameEvent) {
	if v := evt.GetStateVersion(); v > 0 {
		state.Version = v
	}
	switch e := evt.Event.(type) {

	// ── Table lifecycle ────────────────────────────────────────────────────────

	case *pb.GameEvent_TableCreated:
		state.Config = e.TableCreated.GetConfig()
		state.Status = pb.GameStatus_GAME_STATUS_WAITING
		ensureMaps(state)

	case *pb.GameEvent_TableClosed:
		state.Status = pb.GameStatus_GAME_STATUS_CLOSED

	case *pb.GameEvent_TablePaused:
		state.Status = pb.GameStatus_GAME_STATUS_PAUSED

	case *pb.GameEvent_TableResumed:
		if state.CurrentHand != nil {
			state.Status = pb.GameStatus_GAME_STATUS_ACTIVE
		} else {
			state.Status = pb.GameStatus_GAME_STATUS_WAITING
		}

	// ── Seat / player lifecycle ────────────────────────────────────────────────

	case *pb.GameEvent_SeatReserved:
		ev := e.SeatReserved
		state.ReservedSeats = append(state.ReservedSeats, ev.GetSeat())

	case *pb.GameEvent_SeatReservationCancelled:
		ev := e.SeatReservationCancelled
		state.ReservedSeats = removeInt32(state.ReservedSeats, ev.GetSeat())

	case *pb.GameEvent_PlayerJoined:
		ev := e.PlayerJoined
		ensureMaps(state)
		state.Players[ev.GetPlayerId()] = &pb.PlayerState{
			PlayerId:   ev.GetPlayerId(),
			Seat:       ev.GetSeat(),
			SeatStatus: pb.SeatStatus_SEAT_STATUS_OCCUPIED,
			Stack:      ev.GetBuyIn(),
		}
		state.PlayerUserIds[ev.GetPlayerId()] = ev.GetUserId()
		state.ReservedSeats = removeInt32(state.ReservedSeats, ev.GetSeat())

	case *pb.GameEvent_PlayerLeft:
		ev := e.PlayerLeft
		delete(state.Players, ev.GetPlayerId())
		delete(state.PlayerUserIds, ev.GetPlayerId())
		state.WaitingList = removeString(state.WaitingList, ev.GetPlayerId())

	case *pb.GameEvent_PlayerSatOut:
		ev := e.PlayerSatOut
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.IsSittingOut = true
			p.SeatStatus = pb.SeatStatus_SEAT_STATUS_SITTING_OUT
		}

	case *pb.GameEvent_PlayerSatIn:
		ev := e.PlayerSatIn
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.IsSittingOut = false
			if p.DisconnectedAt == nil {
				p.SeatStatus = pb.SeatStatus_SEAT_STATUS_OCCUPIED
			}
		}

	case *pb.GameEvent_PlayerDisconnected:
		ev := e.PlayerDisconnected
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.DisconnectedAt = evt.GetOccurredAt()
			p.SeatStatus = pb.SeatStatus_SEAT_STATUS_DISCONNECTED
		}

	case *pb.GameEvent_PlayerReconnected:
		ev := e.PlayerReconnected
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.DisconnectedAt = nil
			if p.IsSittingOut {
				p.SeatStatus = pb.SeatStatus_SEAT_STATUS_SITTING_OUT
			} else {
				p.SeatStatus = pb.SeatStatus_SEAT_STATUS_OCCUPIED
			}
		}

	case *pb.GameEvent_PlayerKicked:
		ev := e.PlayerKicked
		delete(state.Players, ev.GetPlayerId())
		delete(state.PlayerUserIds, ev.GetPlayerId())
		state.WaitingList = removeString(state.WaitingList, ev.GetPlayerId())

	case *pb.GameEvent_WaitingListJoined:
		ev := e.WaitingListJoined
		state.WaitingList = append(state.WaitingList, ev.GetPlayerId())

	case *pb.GameEvent_WaitingListLeft:
		ev := e.WaitingListLeft
		state.WaitingList = removeString(state.WaitingList, ev.GetPlayerId())

	case *pb.GameEvent_SeatChanged:
		ev := e.SeatChanged
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.Seat = ev.GetToSeat()
		}

	case *pb.GameEvent_StackToppedUp:
		ev := e.StackToppedUp
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.Stack = ev.GetNewStack()
		}

	case *pb.GameEvent_AutoRebuyTriggered:
		ev := e.AutoRebuyTriggered
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.Stack = ev.GetNewStack()
		}

	// ── Hand lifecycle ─────────────────────────────────────────────────────────

	case *pb.GameEvent_HandStarted:
		ev := e.HandStarted
		for _, p := range state.Players {
			p.IsFolded = false
			p.IsAllIn = false
			p.CurrentBet = 0
			p.TotalCommittedThisHand = 0
			p.HoleCard_1 = nil
			p.HoleCard_2 = nil
		}
		state.CurrentHand = &pb.HandState{
			HandId:             ev.GetHandId(),
			DealerPlayerId:     ev.GetDealerPlayerId(),
			SmallBlindPlayerId: ev.GetSbPlayerId(),
			BigBlindPlayerId:   ev.GetBbPlayerId(),
			Phase:              pb.HandPhase_HAND_PHASE_PREFLOP,
			StartedAt:          evt.GetOccurredAt(),
		}
		state.Status = pb.GameStatus_GAME_STATUS_ACTIVE

	case *pb.GameEvent_BlindPosted:
		ev := e.BlindPosted
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		amount := ev.GetAmount()
		p.Stack -= amount
		state.CurrentHand.Pot += amount
		p.TotalCommittedThisHand += amount
		if !ev.GetIsDeadBlind() {
			p.CurrentBet += amount
			if p.CurrentBet > state.CurrentHand.CurrentHighestBet {
				state.CurrentHand.CurrentHighestBet = p.CurrentBet
			}
			if ev.GetIsBigBlind() {
				state.CurrentHand.MinRaiseAmount = amount
			}
		}

	case *pb.GameEvent_AntePosted:
		ev := e.AntePosted
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		p.Stack -= ev.GetAmount()
		state.CurrentHand.Pot += ev.GetAmount()
		p.TotalCommittedThisHand += ev.GetAmount()

	case *pb.GameEvent_StraddlePosted:
		ev := e.StraddlePosted
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		amount := ev.GetAmount()
		p.Stack -= amount
		state.CurrentHand.Pot += amount
		p.TotalCommittedThisHand += amount
		p.CurrentBet += amount
		if p.CurrentBet > state.CurrentHand.CurrentHighestBet {
			state.CurrentHand.CurrentHighestBet = p.CurrentBet
			state.CurrentHand.MinRaiseAmount = amount
		}

	case *pb.GameEvent_HoleCardsDealt:
		// broadcast — cards not included in this event

	case *pb.GameEvent_HoleCardsRevealed:
		ev := e.HoleCardsRevealed
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		c1, c2 := ev.GetCard_1(), ev.GetCard_2()
		p.HoleCard_1 = &c1
		p.HoleCard_2 = &c2

	case *pb.GameEvent_FlopDealt:
		ev := e.FlopDealt
		if state.CurrentHand == nil {
			break
		}
		c1, c2, c3 := ev.GetCard_1(), ev.GetCard_2(), ev.GetCard_3()
		state.CurrentHand.FlopCard_1 = &c1
		state.CurrentHand.FlopCard_2 = &c2
		state.CurrentHand.FlopCard_3 = &c3
		state.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_FLOP
		resetStreet(state)

	case *pb.GameEvent_TurnDealt:
		ev := e.TurnDealt
		if state.CurrentHand == nil {
			break
		}
		c := ev.GetCard()
		state.CurrentHand.TurnCard = &c
		state.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_TURN
		resetStreet(state)

	case *pb.GameEvent_RiverDealt:
		ev := e.RiverDealt
		if state.CurrentHand == nil {
			break
		}
		c := ev.GetCard()
		state.CurrentHand.RiverCard = &c
		state.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_RIVER
		resetStreet(state)

	case *pb.GameEvent_RunItTwiceAgreed:
		// agreement only — second board dealt by SecondBoardDealt

	case *pb.GameEvent_SecondBoardDealt:
		ev := e.SecondBoardDealt
		if state.CurrentHand == nil {
			break
		}
		if tc := ev.GetTurnCard(); tc != 0 {
			state.CurrentHand.SecondBoard = append(state.CurrentHand.SecondBoard, tc)
		}
		state.CurrentHand.SecondBoard = append(state.CurrentHand.SecondBoard, ev.GetRiverCard())

	// ── Player actions ─────────────────────────────────────────────────────────

	case *pb.GameEvent_PlayerFolded:
		ev := e.PlayerFolded
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.IsFolded = true
		}

	case *pb.GameEvent_PlayerChecked:
		// check costs nothing

	case *pb.GameEvent_PlayerCalled:
		ev := e.PlayerCalled
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		amount := ev.GetAmount()
		p.Stack -= amount
		state.CurrentHand.Pot += amount
		p.CurrentBet += amount
		p.TotalCommittedThisHand += amount
		if p.Stack == 0 {
			p.IsAllIn = true
		}

	case *pb.GameEvent_PlayerBet:
		ev := e.PlayerBet
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		total := ev.GetAmount()
		delta := total - p.CurrentBet
		p.Stack -= delta
		state.CurrentHand.Pot += delta
		p.CurrentBet = total
		p.TotalCommittedThisHand += delta
		raiseBy := total - state.CurrentHand.CurrentHighestBet
		state.CurrentHand.CurrentHighestBet = total
		state.CurrentHand.LastRaiseAmount = raiseBy
		state.CurrentHand.MinRaiseAmount = raiseBy
		state.CurrentHand.RaisesThisStreet++

	case *pb.GameEvent_PlayerRaised:
		ev := e.PlayerRaised
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		delta := ev.GetTo() - p.CurrentBet
		p.Stack -= delta
		state.CurrentHand.Pot += delta
		p.CurrentBet = ev.GetTo()
		p.TotalCommittedThisHand += delta
		state.CurrentHand.CurrentHighestBet = ev.GetTo()
		state.CurrentHand.LastRaiseAmount = ev.GetRaiseBy()
		state.CurrentHand.MinRaiseAmount = ev.GetRaiseBy()
		state.CurrentHand.RaisesThisStreet++

	case *pb.GameEvent_PlayerAllIn:
		ev := e.PlayerAllIn
		if state.CurrentHand == nil {
			break
		}
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		total := ev.GetAmount()
		delta := total - p.CurrentBet
		p.Stack = 0
		state.CurrentHand.Pot += delta
		p.CurrentBet = total
		p.TotalCommittedThisHand += delta
		p.IsAllIn = true
		if total > state.CurrentHand.CurrentHighestBet {
			raiseBy := total - state.CurrentHand.CurrentHighestBet
			state.CurrentHand.CurrentHighestBet = total
			state.CurrentHand.LastRaiseAmount = raiseBy
			state.CurrentHand.MinRaiseAmount = raiseBy
			state.CurrentHand.RaisesThisStreet++
		}

	case *pb.GameEvent_PlayerShowedCards:
		ev := e.PlayerShowedCards
		p := state.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		c1, c2 := ev.GetCard_1(), ev.GetCard_2()
		p.HoleCard_1 = &c1
		p.HoleCard_2 = &c2

	case *pb.GameEvent_PlayerMuckedCards:
		ev := e.PlayerMuckedCards
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.HoleCard_1 = nil
			p.HoleCard_2 = nil
		}

	// ── Timing ────────────────────────────────────────────────────────────────

	case *pb.GameEvent_ActionStarted:
		ev := e.ActionStarted
		if state.CurrentHand == nil {
			break
		}
		pid := ev.GetPlayerId()
		state.CurrentHand.ActingPlayerId = &pid
		state.CurrentHand.ActionDeadline = ev.GetDeadline()

	case *pb.GameEvent_TimeBankUsed:
		ev := e.TimeBankUsed
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.TimeBankRemaining = ev.GetTimeBankRemaining()
		}

	case *pb.GameEvent_PlayerTimedOut:
		// default action follows as a separate event

	// ── Pot and showdown ──────────────────────────────────────────────────────

	case *pb.GameEvent_BettingRoundEnded:
		if state.CurrentHand == nil {
			break
		}
		for _, p := range state.Players {
			p.CurrentBet = 0
		}
		state.CurrentHand.CurrentHighestBet = 0
		state.CurrentHand.LastRaiseAmount = 0
		state.CurrentHand.RaisesThisStreet = 0
		state.CurrentHand.ActingPlayerId = nil

	case *pb.GameEvent_SidePotCreated:
		ev := e.SidePotCreated
		if state.CurrentHand == nil {
			break
		}
		state.CurrentHand.SidePots = append(state.CurrentHand.SidePots, &pb.SidePot{
			Amount:            ev.GetAmount(),
			EligiblePlayerIds: ev.GetEligiblePlayerIds(),
		})

	case *pb.GameEvent_Showdown:
		if state.CurrentHand != nil {
			state.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_SHOWDOWN
		}

	case *pb.GameEvent_PotAwarded:
		ev := e.PotAwarded
		winners := ev.GetWinnerPlayerIds()
		if len(winners) == 0 {
			break
		}
		total := ev.GetAmount()
		share := total / int64(len(winners))
		remainder := total % int64(len(winners))
		for i, pid := range winners {
			if p := state.Players[pid]; p != nil {
				award := share
				if i == 0 {
					award += remainder
				}
				p.Stack += award
			}
		}
		if state.CurrentHand != nil {
			state.CurrentHand.Pot -= total
		}

	case *pb.GameEvent_RakeTaken:
		ev := e.RakeTaken
		if state.CurrentHand == nil {
			break
		}
		state.CurrentHand.RakeTaken += ev.GetAmount()
		state.CurrentHand.Pot -= ev.GetAmount()

	case *pb.GameEvent_HandEnded:
		ev := e.HandEnded
		state.HandNumber = ev.GetHandNumber()
		state.CurrentHand = nil
		if state.Status == pb.GameStatus_GAME_STATUS_ACTIVE {
			state.Status = pb.GameStatus_GAME_STATUS_WAITING
		}

	// ── Money flow ────────────────────────────────────────────────────────────

	case *pb.GameEvent_BuyInCompleted:
		ev := e.BuyInCompleted
		if p := state.Players[ev.GetPlayerId()]; p != nil {
			p.Stack += ev.GetAmount()
		}

	// ── No-ops ────────────────────────────────────────────────────────────────

	case *pb.GameEvent_BuyInRequested,
		*pb.GameEvent_BuyInFailed,
		*pb.GameEvent_CashOutRequested,
		*pb.GameEvent_CashOutCompleted,
		*pb.GameEvent_ChatMessageSent,
		*pb.GameEvent_PlayerChatMuted,
		*pb.GameEvent_PlayerEmoteSent,
		*pb.GameEvent_CommandRejected,
		*pb.GameEvent_CommandAccepted:
	}
}

func resetStreet(state *pb.GameState) {
	for _, p := range state.Players {
		p.CurrentBet = 0
	}
	state.CurrentHand.CurrentHighestBet = 0
	state.CurrentHand.LastRaiseAmount = 0
	state.CurrentHand.MinRaiseAmount = 0
	state.CurrentHand.RaisesThisStreet = 0
	state.CurrentHand.ActingPlayerId = nil
}

func ensureMaps(state *pb.GameState) {
	if state.Players == nil {
		state.Players = make(map[string]*pb.PlayerState)
	}
	if state.PlayerUserIds == nil {
		state.PlayerUserIds = make(map[string]string)
	}
}

func removeInt32(s []int32, v int32) []int32 {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func removeString(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}