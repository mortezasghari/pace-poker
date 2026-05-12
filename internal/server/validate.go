package server

import (
	"errors"
	"fmt"
	"time"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func validateCashGameConfig(cfg *pb.CashGameConfig) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	if cfg.TableName == "" {
		return errors.New("table_name is required")
	}
	if len(cfg.TableName) > 100 {
		return errors.New("table_name must be <= 100 characters")
	}
	if cfg.Variant == pb.GameVariant_GAME_VARIANT_UNSPECIFIED {
		return errors.New("variant is required")
	}
	if cfg.Structure == pb.BettingStructure_BETTING_STRUCTURE_UNSPECIFIED {
		return errors.New("structure is required")
	}
	if !isValidCurrency(cfg.Currency) {
		return fmt.Errorf("invalid currency %q", cfg.Currency)
	}
	if cfg.SmallBlind <= 0 {
		return errors.New("small_blind must be positive")
	}
	if cfg.BigBlind <= cfg.SmallBlind {
		return errors.New("big_blind must be greater than small_blind")
	}
	if cfg.MaxSeats < 2 || cfg.MaxSeats > 10 {
		return errors.New("max_seats must be between 2 and 10")
	}
	if cfg.MinPlayersToStart < 2 || cfg.MinPlayersToStart > cfg.MaxSeats {
		return errors.New("min_players_to_start must be between 2 and max_seats")
	}
	if cfg.MinBuyIn <= 0 {
		return errors.New("min_buy_in must be positive")
	}
	if cfg.MaxBuyIn != 0 && cfg.MaxBuyIn < cfg.MinBuyIn {
		return errors.New("max_buy_in must be >= min_buy_in")
	}
	if cfg.RakeBps < 0 || cfg.RakeBps > 10000 {
		return errors.New("rake_bps must be 0..10000")
	}
	if cfg.Structure == pb.BettingStructure_BETTING_STRUCTURE_FIXED_LIMIT {
		if cfg.SmallBet <= 0 || cfg.BigBet <= cfg.SmallBet {
			return errors.New("fixed limit requires small_bet > 0 and big_bet > small_bet")
		}
		if cfg.MaxRaisesPerStreet < 1 {
			return errors.New("fixed limit requires max_raises_per_street >= 1")
		}
	}
	if cfg.IsPrivate && cfg.Password == "" {
		return errors.New("private tables require a password")
	}
	return nil
}

func isValidCurrency(c string) bool {
	switch c {
	case "USD", "EUR", "GBP", "CAD", "AUD", "JPY", "PLAY":
		return true
	}
	return false
}

func timestampFromTime(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}