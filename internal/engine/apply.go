package engine

import (
	"slices"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"google.golang.org/protobuf/proto"
)

// Apply returns a new GameState with the event applied. The input state is
// never mutated. Apply is a pure function: same inputs → same output.
func Apply(state *pb.GameState, evt *pb.GameEvent) *pb.GameState {
	next := proto.Clone(state).(*pb.GameState)
	if v := evt.GetStateVersion(); v > 0 {
		next.Version = v
	} else {
		next.Version++
	}
	switch e := evt.Event.(type) {
	case *pb.GameEvent_TableCreated:
		next.Config = e.TableCreated.GetConfig()
		next.Status = pb.GameStatus_GAME_STATUS_WAITING
		if next.Players == nil {
			next.Players = make(map[string]*pb.PlayerState)
		}
		if next.PlayerUserIds == nil {
			next.PlayerUserIds = make(map[string]string)
		}
	case *pb.GameEvent_TableClosed:
		next.Status = pb.GameStatus_GAME_STATUS_CLOSED
	case *pb.GameEvent_TablePaused:
		next.Status = pb.GameStatus_GAME_STATUS_PAUSED
	case *pb.GameEvent_TableResumed:
		if next.CurrentHand != nil {
			next.Status = pb.GameStatus_GAME_STATUS_ACTIVE
		} else {
			next.Status = pb.GameStatus_GAME_STATUS_WAITING
		}

	// ── Seat / player lifecycle ───────────────────────────────────────────────

	case *pb.GameEvent_SeatReserved:
		ev := e.SeatReserved
		next.ReservedSeats = append(next.ReservedSeats, ev.GetSeat())
	case *pb.GameEvent_SeatReservationCancelled:
		ev := e.SeatReservationCancelled
		next.ReservedSeats = removeInt32(next.ReservedSeats, ev.GetSeat())
	case *pb.GameEvent_PlayerJoined:
		ev := e.PlayerJoined
		if next.Players == nil {
			next.Players = make(map[string]*pb.PlayerState)
		}
		if next.PlayerUserIds == nil {
			next.PlayerUserIds = make(map[string]string)
		}
		next.Players[ev.GetPlayerId()] = &pb.PlayerState{
			PlayerId:   ev.GetPlayerId(),
			Seat:       ev.GetSeat(),
			SeatStatus: pb.SeatStatus_SEAT_STATUS_OCCUPIED,
			Stack:      ev.GetBuyIn(),
		}
		next.PlayerUserIds[ev.GetPlayerId()] = ev.GetUserId()
		next.ReservedSeats = removeInt32(next.ReservedSeats, ev.GetSeat())
	case *pb.GameEvent_PlayerLeft:
		ev := e.PlayerLeft
		delete(next.Players, ev.GetPlayerId())
		delete(next.PlayerUserIds, ev.GetPlayerId())
		next.WaitingList = removeString(next.WaitingList, ev.GetPlayerId())
	case *pb.GameEvent_PlayerSatOut:
		ev := e.PlayerSatOut
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.IsSittingOut = true
			p.SeatStatus = pb.SeatStatus_SEAT_STATUS_SITTING_OUT
		}
	case *pb.GameEvent_PlayerSatIn:
		ev := e.PlayerSatIn
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.IsSittingOut = false
			if p.DisconnectedAt == nil {
				p.SeatStatus = pb.SeatStatus_SEAT_STATUS_OCCUPIED
			}
		}
	case *pb.GameEvent_PlayerDisconnected:
		ev := e.PlayerDisconnected
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.DisconnectedAt = evt.GetOccurredAt()
			p.SeatStatus = pb.SeatStatus_SEAT_STATUS_DISCONNECTED
		}
	case *pb.GameEvent_PlayerReconnected:
		ev := e.PlayerReconnected
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.DisconnectedAt = nil
			if p.IsSittingOut {
				p.SeatStatus = pb.SeatStatus_SEAT_STATUS_SITTING_OUT
			} else {
				p.SeatStatus = pb.SeatStatus_SEAT_STATUS_OCCUPIED
			}
		}
	case *pb.GameEvent_PlayerKicked:
		ev := e.PlayerKicked
		delete(next.Players, ev.GetPlayerId())
		delete(next.PlayerUserIds, ev.GetPlayerId())
		next.WaitingList = removeString(next.WaitingList, ev.GetPlayerId())
	case *pb.GameEvent_WaitingListJoined:
		next.WaitingList = append(next.WaitingList, e.WaitingListJoined.GetPlayerId())
	case *pb.GameEvent_WaitingListLeft:
		next.WaitingList = removeString(next.WaitingList, e.WaitingListLeft.GetPlayerId())
	case *pb.GameEvent_SeatChanged:
		ev := e.SeatChanged
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.Seat = ev.GetToSeat()
		}
	case *pb.GameEvent_StackToppedUp:
		ev := e.StackToppedUp
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.Stack = ev.GetNewStack()
		}
	case *pb.GameEvent_AutoRebuyTriggered:
		ev := e.AutoRebuyTriggered
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.Stack = ev.GetNewStack()
		}

	// ── Hand lifecycle ────────────────────────────────────────────────────────

	case *pb.GameEvent_HandStarted:
		ev := e.HandStarted
		for _, p := range next.Players {
			p.IsFolded = false
			p.IsAllIn = false
			p.CurrentBet = 0
			p.TotalCommittedThisHand = 0
			p.HoleCard_1 = nil
			p.HoleCard_2 = nil
		}
		// Compute action order: seats sorted ascending starting after dealer,
		// wrapping around. PendingActions starts as the full order (blinds
		// included; they'll call/check at the end of the preflop round).
		order := preflopActionOrder(next, ev.GetDealerPlayerId(), ev.GetBbPlayerId())
		next.CurrentHand = &pb.HandState{
			HandId:             ev.GetHandId(),
			DealerPlayerId:     ev.GetDealerPlayerId(),
			SmallBlindPlayerId: ev.GetSbPlayerId(),
			BigBlindPlayerId:   ev.GetBbPlayerId(),
			Phase:              pb.HandPhase_HAND_PHASE_PREFLOP,
			StartedAt:          evt.GetOccurredAt(),
			ActionOrder:        order,
			PendingActions:     slices.Clone(order),
			MinRaiseAmount:     ev.GetBigBlind(),
		}
		next.Status = pb.GameStatus_GAME_STATUS_ACTIVE
		if p := next.Players[ev.GetDealerPlayerId()]; p != nil {
			next.DealerSeat = p.Seat
		}

	case *pb.GameEvent_BlindPosted:
		ev := e.BlindPosted
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		amount := ev.GetAmount()
		p.Stack -= amount
		next.CurrentHand.Pot += amount
		p.TotalCommittedThisHand += amount
		if !ev.GetIsDeadBlind() {
			p.CurrentBet += amount
			if p.CurrentBet > next.CurrentHand.CurrentHighestBet {
				next.CurrentHand.CurrentHighestBet = p.CurrentBet
			}
			if ev.GetIsBigBlind() {
				next.CurrentHand.MinRaiseAmount = amount
				next.CurrentHand.LastRaiseAmount = amount
			}
		}

	case *pb.GameEvent_AntePosted:
		ev := e.AntePosted
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		p.Stack -= ev.GetAmount()
		next.CurrentHand.Pot += ev.GetAmount()
		p.TotalCommittedThisHand += ev.GetAmount()

	case *pb.GameEvent_StraddlePosted:
		ev := e.StraddlePosted
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		amount := ev.GetAmount()
		p.Stack -= amount
		next.CurrentHand.Pot += amount
		p.TotalCommittedThisHand += amount
		p.CurrentBet += amount
		if p.CurrentBet > next.CurrentHand.CurrentHighestBet {
			next.CurrentHand.CurrentHighestBet = p.CurrentBet
			next.CurrentHand.MinRaiseAmount = amount
		}

	case *pb.GameEvent_HoleCardsDealt:
		// Broadcast event — card values not present; no state change needed.

	case *pb.GameEvent_HoleCardsRevealed:
		ev := e.HoleCardsRevealed
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		c1, c2 := ev.GetCard_1(), ev.GetCard_2()
		p.HoleCard_1 = &c1
		p.HoleCard_2 = &c2

	case *pb.GameEvent_FlopDealt:
		ev := e.FlopDealt
		if next.CurrentHand == nil {
			break
		}
		c1, c2, c3 := ev.GetCard_1(), ev.GetCard_2(), ev.GetCard_3()
		next.CurrentHand.FlopCard_1 = &c1
		next.CurrentHand.FlopCard_2 = &c2
		next.CurrentHand.FlopCard_3 = &c3
		next.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_FLOP
		resetStreetState(next)

	case *pb.GameEvent_TurnDealt:
		ev := e.TurnDealt
		if next.CurrentHand == nil {
			break
		}
		c := ev.GetCard()
		next.CurrentHand.TurnCard = &c
		next.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_TURN
		resetStreetState(next)

	case *pb.GameEvent_RiverDealt:
		ev := e.RiverDealt
		if next.CurrentHand == nil {
			break
		}
		c := ev.GetCard()
		next.CurrentHand.RiverCard = &c
		next.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_RIVER
		resetStreetState(next)

	case *pb.GameEvent_RunItTwiceAgreed:
		// Agreement — second board dealt separately.

	case *pb.GameEvent_SecondBoardDealt:
		ev := e.SecondBoardDealt
		if next.CurrentHand == nil {
			break
		}
		if tc := ev.GetTurnCard(); tc != 0 {
			next.CurrentHand.SecondBoard = append(next.CurrentHand.SecondBoard, tc)
		}
		next.CurrentHand.SecondBoard = append(next.CurrentHand.SecondBoard, ev.GetRiverCard())

	// ── Player actions ────────────────────────────────────────────────────────

	case *pb.GameEvent_PlayerFolded:
		ev := e.PlayerFolded
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.IsFolded = true
		}
		if next.CurrentHand != nil {
			next.CurrentHand.PendingActions = removeString(next.CurrentHand.PendingActions, ev.GetPlayerId())
			next.CurrentHand.ActingPlayerId = nil
		}

	case *pb.GameEvent_PlayerChecked:
		ev := e.PlayerChecked
		if next.CurrentHand != nil {
			next.CurrentHand.PendingActions = removeString(next.CurrentHand.PendingActions, ev.GetPlayerId())
			next.CurrentHand.ActingPlayerId = nil
		}

	case *pb.GameEvent_PlayerCalled:
		ev := e.PlayerCalled
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		amount := ev.GetAmount()
		p.Stack -= amount
		next.CurrentHand.Pot += amount
		p.CurrentBet += amount
		p.TotalCommittedThisHand += amount
		if p.Stack == 0 {
			p.IsAllIn = true
		}
		next.CurrentHand.PendingActions = removeString(next.CurrentHand.PendingActions, ev.GetPlayerId())
		next.CurrentHand.ActingPlayerId = nil

	case *pb.GameEvent_PlayerBet:
		ev := e.PlayerBet
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		total := ev.GetAmount()
		delta := total - p.CurrentBet
		p.Stack -= delta
		next.CurrentHand.Pot += delta
		p.CurrentBet = total
		p.TotalCommittedThisHand += delta
		raiseBy := total - next.CurrentHand.CurrentHighestBet
		next.CurrentHand.CurrentHighestBet = total
		next.CurrentHand.LastRaiseAmount = raiseBy
		next.CurrentHand.MinRaiseAmount = raiseBy
		next.CurrentHand.LastFullRaiseTo = total
		next.CurrentHand.ActionClosedFor = nil
		next.CurrentHand.RaisesThisStreet++
		// Everyone except the bettor must now act.
		next.CurrentHand.PendingActions = othersInOrder(next, ev.GetPlayerId())
		next.CurrentHand.ActingPlayerId = nil

	case *pb.GameEvent_PlayerRaised:
		ev := e.PlayerRaised
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		delta := ev.GetTo() - p.CurrentBet
		p.Stack -= delta
		next.CurrentHand.Pot += delta
		p.CurrentBet = ev.GetTo()
		p.TotalCommittedThisHand += delta
		if p.Stack == 0 {
			p.IsAllIn = true
		}
		next.CurrentHand.CurrentHighestBet = ev.GetTo()
		next.CurrentHand.LastRaiseAmount = ev.GetRaiseBy()
		next.CurrentHand.MinRaiseAmount = ev.GetRaiseBy()
		next.CurrentHand.LastFullRaiseTo = ev.GetTo()
		next.CurrentHand.ActionClosedFor = nil
		next.CurrentHand.RaisesThisStreet++
		next.CurrentHand.PendingActions = othersInOrder(next, ev.GetPlayerId())
		next.CurrentHand.ActingPlayerId = nil

	case *pb.GameEvent_PlayerAllIn:
		ev := e.PlayerAllIn
		if next.CurrentHand == nil {
			break
		}
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		actor := ev.GetPlayerId()
		total := ev.GetAmount()
		delta := total - p.CurrentBet
		p.Stack = 0
		next.CurrentHand.Pot += delta
		p.CurrentBet = total
		p.TotalCommittedThisHand += delta
		p.IsAllIn = true
		if total > next.CurrentHand.CurrentHighestBet {
			// Save last raise size before overwriting — used to detect sub-min raises.
			prevLastRaise := next.CurrentHand.LastRaiseAmount
			raiseBy := total - next.CurrentHand.CurrentHighestBet
			next.CurrentHand.CurrentHighestBet = total
			next.CurrentHand.LastRaiseAmount = raiseBy
			next.CurrentHand.MinRaiseAmount = raiseBy
			next.CurrentHand.RaisesThisStreet++
			allOthers := othersInOrder(next, actor)
			if prevLastRaise == 0 || raiseBy >= prevLastRaise {
				// Full raise (or first bet this street): reopen action for everyone.
				next.CurrentHand.LastFullRaiseTo = total
				next.CurrentHand.ActionClosedFor = nil
			} else {
				// Sub-minimum all-in raise: close raise option for players who already acted.
				// "Already acted" = non-folded, non-all-in players NOT in PendingActions.
				pendingSet := make(map[string]bool, len(next.CurrentHand.PendingActions))
				for _, pid := range next.CurrentHand.PendingActions {
					if pid != actor {
						pendingSet[pid] = true
					}
				}
				var closed []string
				for _, pid := range allOthers {
					if !pendingSet[pid] {
						closed = append(closed, pid)
					}
				}
				next.CurrentHand.ActionClosedFor = closed
			}
			next.CurrentHand.PendingActions = allOthers
		} else {
			next.CurrentHand.PendingActions = removeString(next.CurrentHand.PendingActions, actor)
		}
		next.CurrentHand.ActingPlayerId = nil

	case *pb.GameEvent_PlayerShowedCards:
		ev := e.PlayerShowedCards
		p := next.Players[ev.GetPlayerId()]
		if p == nil {
			break
		}
		c1, c2 := ev.GetCard_1(), ev.GetCard_2()
		p.HoleCard_1 = &c1
		p.HoleCard_2 = &c2

	case *pb.GameEvent_PlayerMuckedCards:
		ev := e.PlayerMuckedCards
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.HoleCard_1 = nil
			p.HoleCard_2 = nil
		}

	// ── Timing ───────────────────────────────────────────────────────────────

	case *pb.GameEvent_ActionStarted:
		ev := e.ActionStarted
		if next.CurrentHand == nil {
			break
		}
		pid := ev.GetPlayerId()
		next.CurrentHand.ActingPlayerId = &pid
		next.CurrentHand.ActionDeadline = ev.GetDeadline()

	case *pb.GameEvent_TimeBankUsed:
		ev := e.TimeBankUsed
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.TimeBankRemaining = ev.GetTimeBankRemaining()
		}

	case *pb.GameEvent_PlayerTimedOut:
		// Default action follows as a separate event.

	// ── Pot and showdown ─────────────────────────────────────────────────────

	case *pb.GameEvent_BettingRoundEnded:
		if next.CurrentHand == nil {
			break
		}
		for _, p := range next.Players {
			p.CurrentBet = 0
		}
		next.CurrentHand.CurrentHighestBet = 0
		next.CurrentHand.LastRaiseAmount = 0
		next.CurrentHand.LastFullRaiseTo = 0
		next.CurrentHand.ActionClosedFor = nil
		next.CurrentHand.RaisesThisStreet = 0
		next.CurrentHand.ActingPlayerId = nil
		next.CurrentHand.PendingActions = nil

	case *pb.GameEvent_SidePotCreated:
		ev := e.SidePotCreated
		if next.CurrentHand == nil {
			break
		}
		next.CurrentHand.SidePots = append(next.CurrentHand.SidePots, &pb.SidePot{
			Amount:            ev.GetAmount(),
			EligiblePlayerIds: ev.GetEligiblePlayerIds(),
		})

	case *pb.GameEvent_Showdown:
		if next.CurrentHand != nil {
			next.CurrentHand.Phase = pb.HandPhase_HAND_PHASE_SHOWDOWN
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
			if p := next.Players[pid]; p != nil {
				award := share
				if i == 0 {
					award += remainder
				}
				p.Stack += award
			}
		}
		if next.CurrentHand != nil {
			next.CurrentHand.Pot -= total
		}

	case *pb.GameEvent_RakeTaken:
		ev := e.RakeTaken
		if next.CurrentHand == nil {
			break
		}
		next.CurrentHand.RakeTaken += ev.GetAmount()
		next.CurrentHand.Pot -= ev.GetAmount()

	case *pb.GameEvent_HandEnded:
		ev := e.HandEnded
		next.HandNumber = ev.GetHandNumber()
		next.CurrentHand = nil
		if next.Status == pb.GameStatus_GAME_STATUS_ACTIVE {
			next.Status = pb.GameStatus_GAME_STATUS_WAITING
		}

	// ── Money flow ────────────────────────────────────────────────────────────

	case *pb.GameEvent_BuyInCompleted:
		ev := e.BuyInCompleted
		if p := next.Players[ev.GetPlayerId()]; p != nil {
			p.Stack += ev.GetAmount()
		}

	// ── No-ops ───────────────────────────────────────────────────────────────

	case *pb.GameEvent_BuyInRequested,
		*pb.GameEvent_BuyInFailed,
		*pb.GameEvent_CashOutRequested,
		*pb.GameEvent_CashOutCompleted,
		*pb.GameEvent_ChatMessageSent,
		*pb.GameEvent_PlayerChatMuted,
		*pb.GameEvent_PlayerEmoteSent,
		*pb.GameEvent_CommandRejected,
		*pb.GameEvent_CommandAccepted:
		// Informational — no GameState field changes.
	}

	return next
}

// applyAll applies a sequence of events in order, returning the final state.
func applyAll(state *pb.GameState, events []*pb.GameEvent) *pb.GameState {
	for _, evt := range events {
		state = Apply(state, evt)
	}
	return state
}

// ── helpers ──────────────────────────────────────────────────────────────────

// resetStreetState resets per-street betting counters and pending actions.
// Called after each board card is dealt.
func resetStreetState(state *pb.GameState) {
	h := state.CurrentHand
	for _, p := range state.Players {
		p.CurrentBet = 0
	}
	h.CurrentHighestBet = 0
	h.LastRaiseAmount = 0
	// MinRaiseAmount starts at BigBlind so the first bet on a new street must
	// be at least a full BB, and subsequent raise sizing is computed correctly.
	bb := int64(0)
	if state.Config != nil {
		bb = state.Config.BigBlind
	}
	h.MinRaiseAmount = bb
	h.LastFullRaiseTo = 0
	h.ActionClosedFor = nil
	h.RaisesThisStreet = 0
	h.ActingPlayerId = nil
	// Set pending actions to all active (not folded, not all-in) players in
	// post-flop order (SB first, then clockwise to BTN).
	h.ActionOrder = postflopActionOrder(state, h.DealerPlayerId)
	h.PendingActions = slices.Clone(h.ActionOrder)
}

// preflopActionOrder returns player IDs in preflop action order:
// UTG (first after BB) → ... → BTN → SB → BB.
func preflopActionOrder(state *pb.GameState, dealerID, bbID string) []string {
	seated := seatedActivePlayersBySeats(state)
	if len(seated) == 0 {
		return nil
	}
	// Find BB's position in seated list.
	bbIdx := -1
	for i, pid := range seated {
		if pid == bbID {
			bbIdx = i
			break
		}
	}
	if bbIdx == -1 {
		// Fallback: start from dealer+1.
		return rotateAfter(seated, dealerID)
	}
	// Rotate so UTG (after BB) is first; BB is last.
	return rotateFrom(seated, (bbIdx+1)%len(seated))
}

// postflopActionOrder returns player IDs in post-flop action order:
// first active player left of dealer, clockwise.
func postflopActionOrder(state *pb.GameState, dealerID string) []string {
	seated := seatedActivePlayersBySeats(state)
	return rotateAfter(seated, dealerID)
}

// seatedActivePlayersBySeats returns IDs of seated, sitting-in players with a
// positive stack, sorted by seat number (ascending). Zero-stack players are
// excluded — they will be auto-sat-out by advance before the next hand.
func seatedActivePlayersBySeats(state *pb.GameState) []string {
	type sp struct {
		seat int32
		id   string
	}
	var players []sp
	for id, p := range state.Players {
		if !p.IsSittingOut && p.Stack > 0 {
			players = append(players, sp{p.Seat, id})
		}
	}
	slices.SortFunc(players, func(a, b sp) int {
		if a.seat < b.seat {
			return -1
		}
		if a.seat > b.seat {
			return 1
		}
		return 0
	})
	out := make([]string, len(players))
	for i, p := range players {
		out[i] = p.id
	}
	return out
}

// rotateAfter returns the list rotated so the player after targetID is first.
func rotateAfter(ids []string, targetID string) []string {
	if len(ids) == 0 {
		return nil
	}
	for i, id := range ids {
		if id == targetID {
			return rotateFrom(ids, (i+1)%len(ids))
		}
	}
	return slices.Clone(ids)
}

// rotateFrom returns the list rotated so ids[start] is first.
func rotateFrom(ids []string, start int) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i := range ids {
		out[i] = ids[(start+i)%len(ids)]
	}
	return out
}

// othersInOrder returns all non-folded, non-all-in players in ActionOrder
// excluding exceptID. Used after a bet/raise to reset PendingActions.
func othersInOrder(state *pb.GameState, exceptID string) []string {
	if state.CurrentHand == nil {
		return nil
	}
	var out []string
	for _, pid := range state.CurrentHand.ActionOrder {
		if pid == exceptID {
			continue
		}
		p := state.Players[pid]
		if p == nil || p.IsFolded || p.IsAllIn {
			continue
		}
		out = append(out, pid)
	}
	return out
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