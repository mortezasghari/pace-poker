// Package lobby implements the cross-domain join/leave operations that span
// both the user wallet (store) and the game event log (store + engine).
// It lives in its own package to avoid an import cycle: engine imports store,
// so store cannot import engine; but lobby can import both.
package lobby

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/engine"
	"github.com/pacepoker/poker/internal/store"
)

// JoinTable atomically debits the user's chip balance, validates the join,
// appends a PlayerJoined event, and returns the resulting state.
//
// All writes occur in a single Postgres transaction. On ErrConcurrentWrite the
// caller should retry (with the same CommandID for idempotency).
func JoinTable(ctx context.Context, st store.Store, in store.JoinTableInput) (*store.JoinTableResult, error) {
	var result *store.JoinTableResult

	err := st.WithTx(ctx, func(tx store.Store) error {
		// 1. Idempotency: return early if this command was already processed.
		if in.CommandID != uuid.Nil {
			existing, err := tx.FindEventByCommandID(ctx, in.CommandID, in.GameID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("idempotency check: %w", err)
			}
			if err == nil {
				state, _, err := engine.LoadState(ctx, tx, in.GameID)
				if err != nil {
					return fmt.Errorf("load state for idempotency replay: %w", err)
				}
				snap, err := tx.GetUserSnapshot(ctx, in.UserID)
				if err != nil {
					return fmt.Errorf("user snapshot for idempotency replay: %w", err)
				}
				result = &store.JoinTableResult{
					GameState:      state,
					NewSequence:    existing.GetSequence(),
					UserNewBalance: snap.ChipBalance,
				}
				return nil
			}
		}

		// 2. Load current game state and latest sequence number.
		state, latestSeq, err := engine.LoadState(ctx, tx, in.GameID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.ErrNotFound
			}
			return fmt.Errorf("load game state: %w", err)
		}

		// 3. Validate game is joinable.
		switch state.GetStatus() {
		case pb.GameStatus_GAME_STATUS_FINISHED, pb.GameStatus_GAME_STATUS_CLOSED:
			return store.ErrGameClosed
		}

		// 4. Validate buy-in range.
		cfg := state.GetConfig()
		if in.BuyIn < cfg.GetMinBuyIn() {
			return fmt.Errorf("%w: %d < min %d", store.ErrBuyInOutOfRange, in.BuyIn, cfg.GetMinBuyIn())
		}
		if cfg.GetMaxBuyIn() > 0 && in.BuyIn > cfg.GetMaxBuyIn() {
			return fmt.Errorf("%w: %d > max %d", store.ErrBuyInOutOfRange, in.BuyIn, cfg.GetMaxBuyIn())
		}

		// 5. Ensure the user is not already seated.
		if _, ok := findPlayerByUserID(state, in.UserID.String()); ok {
			return store.ErrAlreadySeated
		}

		// 6. Determine seat (auto or requested).
		seat, err := selectSeat(state, in.Seat)
		if err != nil {
			return err
		}

		// 7. Debit user balance — WHERE balance >= amount acts as an atomic
		//    insufficient-funds guard; ErrNoRows → ErrInsufficientBalance.
		userSnap, err := tx.DebitUserBalance(ctx, in.UserID, in.BuyIn)
		if err != nil {
			if errors.Is(err, store.ErrInsufficientBalance) {
				return store.ErrInsufficientFunds
			}
			if errors.Is(err, store.ErrNotFound) {
				return store.ErrNotFound
			}
			return fmt.Errorf("debit user balance: %w", err)
		}

		// 8. Build and append the PlayerJoined event.
		nextSeq := latestSeq + 1
		nextVersion := state.GetVersion() + 1
		playerID := in.UserID.String() // lobby joins use user_id as player_id
		evt := engine.BuildPlayerJoinedEvent(
			in.GameID, nextSeq, in.CommandID,
			playerID, in.UserID.String(),
			seat, in.BuyIn, nextVersion,
		)
		if err := tx.AppendEvent(ctx, evt); err != nil {
			return err // ErrConcurrentWrite propagates; caller retries
		}

		// 9. Apply event to produce the returned state.
		updatedState := engine.Apply(state, evt)

		result = &store.JoinTableResult{
			GameState:      updatedState,
			NewSequence:    nextSeq,
			UserNewBalance: userSnap.ChipBalance,
		}
		return nil
	})

	return result, err
}

// LeaveTable atomically appends a PlayerLeft event (and PlayerFolded if the
// player is in an active hand), credits the remaining stack to the user's
// balance, and returns the cash-out amount and new balance.
func LeaveTable(ctx context.Context, st store.Store, in store.LeaveTableInput) (*store.LeaveTableResult, error) {
	var result *store.LeaveTableResult

	err := st.WithTx(ctx, func(tx store.Store) error {
		// 1. Idempotency check.
		if in.CommandID != uuid.Nil {
			existing, err := tx.FindEventByCommandID(ctx, in.CommandID, in.GameID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("idempotency check: %w", err)
			}
			if err == nil {
				state, _, err := engine.LoadState(ctx, tx, in.GameID)
				if err != nil {
					return fmt.Errorf("load state for idempotency replay: %w", err)
				}
				snap, err := tx.GetUserSnapshot(ctx, in.UserID)
				if err != nil {
					return fmt.Errorf("user snapshot for idempotency replay: %w", err)
				}
				// PlayerLeft is the definitive event; PlayerFolded (if any) has
				// the same command_id but 0 cash_out — the PlayerLeft event is
				// what matters for cash-out reporting.
				var cashOut int64
				if pl := existing.GetPlayerLeft(); pl != nil {
					cashOut = pl.GetCashOutAmount()
				}
				result = &store.LeaveTableResult{
					GameState:      state,
					CashOutAmount:  cashOut,
					UserNewBalance: snap.ChipBalance,
				}
				return nil
			}
		}

		// 2. Load current game state.
		state, latestSeq, err := engine.LoadState(ctx, tx, in.GameID)
		if err != nil {
			return fmt.Errorf("load game state: %w", err)
		}

		playerID, ok := findPlayerByUserID(state, in.UserID.String())
		if !ok {
			return store.ErrUserNotAtTable
		}
		player := state.GetPlayers()[playerID]

		// 3. Build event sequence.
		var events []*pb.GameEvent
		nextSeq := latestSeq + 1
		nextVersion := state.GetVersion() + 1

		// Auto-fold if the player is in an active hand and not yet folded.
		// Their current_bet is already committed to the pot (it stays there).
		if state.GetCurrentHand() != nil && !player.GetIsFolded() {
			foldEvt := engine.BuildAutoFoldEvent(in.GameID, nextSeq, in.CommandID, playerID, nextVersion)
			events = append(events, foldEvt)
			nextSeq++
			nextVersion++
		}

		// Cash-out = remaining stack (current_bet is forfeited, not returned).
		cashOut := player.GetStack()
		leaveEvt := engine.BuildPlayerLeftEvent(in.GameID, nextSeq, in.CommandID, playerID, cashOut, nextVersion)
		events = append(events, leaveEvt)

		// 4. Append events atomically.
		if err := tx.AppendEvents(ctx, events); err != nil {
			return err
		}

		// 5. Credit the cash-out amount back to the user's balance.
		var userSnap store.UserSnapshot
		if cashOut > 0 {
			userSnap, err = tx.CreditUserBalance(ctx, in.UserID, cashOut)
			if err != nil {
				return fmt.Errorf("credit user balance: %w", err)
			}
		} else {
			userSnap, err = tx.GetUserSnapshot(ctx, in.UserID)
			if err != nil {
				return fmt.Errorf("get user snapshot: %w", err)
			}
		}

		// 6. Apply events to produce the returned state.
		updatedState := state
		for _, ev := range events {
			updatedState = engine.Apply(updatedState, ev)
		}

		result = &store.LeaveTableResult{
			GameState:      updatedState,
			CashOutAmount:  cashOut,
			UserNewBalance: userSnap.ChipBalance,
		}
		return nil
	})

	return result, err
}

// ── Private helpers ───────────────────────────────────────────────────────────

// findPlayerByUserID returns the player_id for the given user_id string, if seated.
// GameState.PlayerUserIds maps player_id → user_id.
func findPlayerByUserID(state *pb.GameState, userID string) (string, bool) {
	for pid, uid := range state.GetPlayerUserIds() {
		if uid == userID {
			return pid, true
		}
	}
	return "", false
}

// selectSeat returns an available seat number for the join.
// requestedSeat == 0 means pick the lowest available seat automatically.
func selectSeat(state *pb.GameState, requestedSeat int32) (int32, error) {
	occupied := make(map[int32]bool, len(state.GetPlayers()))
	for _, p := range state.GetPlayers() {
		occupied[p.GetSeat()] = true
	}
	reserved := make(map[int32]bool, len(state.GetReservedSeats()))
	for _, s := range state.GetReservedSeats() {
		reserved[s] = true
	}
	maxSeats := state.GetConfig().GetMaxSeats()

	if requestedSeat > 0 {
		if requestedSeat > maxSeats || occupied[requestedSeat] || reserved[requestedSeat] {
			return 0, store.ErrSeatTaken
		}
		return requestedSeat, nil
	}

	for s := int32(1); s <= maxSeats; s++ {
		if !occupied[s] && !reserved[s] {
			return s, nil
		}
	}
	return 0, store.ErrTableFull
}
