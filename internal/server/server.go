package server

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/auth"
	"github.com/pacepoker/poker/internal/lobby"
	"github.com/pacepoker/poker/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CommandSubmitter is satisfied by *engine.Router. Exported so integration
// tests can inject a fake router without depending on the concrete type.
type CommandSubmitter interface {
	Submit(ctx context.Context, gameID uuid.UUID, actorPlayerID string, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error)
	Invalidate(gameID uuid.UUID)
}

// Server implements pb.PokerServiceServer.
type Server struct {
	pb.UnimplementedPokerServiceServer
	store  store.Store
	router CommandSubmitter
	auth   *auth.Authenticator
}

// NewServer returns a Server wired to the given store, router, and authenticator.
func NewServer(st store.Store, router CommandSubmitter, a *auth.Authenticator) *Server {
	return &Server{store: st, router: router, auth: a}
}

// ── Lobby ─────────────────────────────────────────────────────────────────────

func (s *Server) CreateTable(ctx context.Context, req *pb.CreateTableCommand) (*pb.CreateTableResponse, error) {
	if err := validateCashGameConfig(req.GetConfig()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid config: %v", err)
	}

	cmdID, err := parseOrGenUUID(req.GetCommandId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid command_id: %v", err)
	}

	// Idempotency: if we've already processed this command, return the same result.
	if cmdID != uuid.Nil {
		if existing, err := s.store.FindEventByCommandIDGlobal(ctx, cmdID); err == nil {
			gameID, _ := uuid.Parse(existing.GetGameId())
			snap, _, snapErr := s.store.GetLatestSnapshot(ctx, gameID)
			if snapErr != nil {
				log.Printf("idempotency: found event but no snapshot for game %s: %v", gameID, snapErr)
				return nil, status.Error(codes.Internal, "idempotency replay failed")
			}
			return &pb.CreateTableResponse{State: snap}, nil
		}
	}

	state, err := s.store.CreateTable(ctx, req.GetConfig(), cmdID)
	if err != nil {
		log.Printf("CreateTable: %v", err)
		return nil, status.Error(codes.Internal, "failed to create table")
	}
	return &pb.CreateTableResponse{State: state}, nil
}

func (s *Server) SearchTables(ctx context.Context, req *pb.SearchTablesRequest) (*pb.SearchTablesResponse, error) {
	if req.GetOffset() < 0 {
		return nil, status.Error(codes.InvalidArgument, "offset must be >= 0")
	}

	limit := req.GetLimit()
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}

	filter := store.SearchFilter{
		NameQuery: req.GetNameQuery(),
		BigBlind:  req.GetBigBlind(),
		Variant:   req.GetVariant(),
		Structure: req.GetStructure(),
		Limit:     limit,
		Offset:    req.GetOffset(),
	}

	summaries, total, err := s.store.SearchGameConfigs(ctx, filter)
	if err != nil {
		log.Printf("SearchTables: %v", err)
		return nil, status.Error(codes.Internal, "search failed")
	}

	tables := make([]*pb.TableSummary, len(summaries))
	for i, s := range summaries {
		tables[i] = summaryToProto(s)
	}
	return &pb.SearchTablesResponse{Tables: tables, TotalCount: total}, nil
}

func (s *Server) ReserveSeat(_ context.Context, _ *pb.ReserveSeatCommand) (*pb.ReserveSeatResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ReserveSeat not implemented")
}

func (s *Server) CancelSeatReservation(_ context.Context, _ *pb.CancelSeatReservationCommand) (*pb.CancelSeatReservationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CancelSeatReservation not implemented")
}

func (s *Server) JoinTable(ctx context.Context, req *pb.JoinTableCommand) (*pb.JoinTableResponse, error) {
	gameID, err := uuid.Parse(req.GetGameId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid game_id: %v", err)
	}
	userID, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetBuyIn() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "buy_in must be positive")
	}
	if req.GetSeat() < 0 {
		return nil, status.Error(codes.InvalidArgument, "seat must be >= 0 (0 = auto)")
	}
	cmdID, err := parseOrGenUUID(req.GetCommandId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid command_id: %v", err)
	}

	in := store.JoinTableInput{
		CommandID: cmdID,
		GameID:    gameID,
		UserID:    userID,
		Seat:      req.GetSeat(),
		BuyIn:     req.GetBuyIn(),
	}
	var result *store.JoinTableResult
	retryOnConcurrentWrite(ctx, 3, func() error {
		result, err = lobby.JoinTable(ctx, s.store, in)
		return err
	})
	if err != nil {
		return nil, mapLobbyError(err)
	}

	s.router.Invalidate(gameID)
	return &pb.JoinTableResponse{State: result.GameState}, nil
}

func (s *Server) LeaveTable(ctx context.Context, req *pb.LeaveTableCommand) (*pb.LeaveTableResponse, error) {
	gameID, err := uuid.Parse(req.GetGameId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid game_id: %v", err)
	}
	userID, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cmdID, err := parseOrGenUUID(req.GetCommandId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid command_id: %v", err)
	}

	in := store.LeaveTableInput{
		CommandID: cmdID,
		GameID:    gameID,
		UserID:    userID,
	}
	var result *store.LeaveTableResult
	retryOnConcurrentWrite(ctx, 3, func() error {
		result, err = lobby.LeaveTable(ctx, s.store, in)
		return err
	})
	if err != nil {
		return nil, mapLobbyError(err)
	}

	s.router.Invalidate(gameID)
	return &pb.LeaveTableResponse{CashOutAmount: result.CashOutAmount}, nil
}

// retryOnConcurrentWrite calls fn up to maxAttempts times, retrying only on
// ErrConcurrentWrite. The same command_id makes retries idempotent.
func retryOnConcurrentWrite(ctx context.Context, maxAttempts int, fn func() error) {
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); !errors.Is(err, store.ErrConcurrentWrite) {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func mapLobbyError(err error) error {
	switch {
	case errors.Is(err, store.ErrInsufficientFunds):
		return status.Error(codes.FailedPrecondition, "insufficient funds")
	case errors.Is(err, store.ErrSeatTaken):
		return status.Error(codes.FailedPrecondition, "seat already taken")
	case errors.Is(err, store.ErrBuyInOutOfRange):
		return status.Errorf(codes.InvalidArgument, "%v", err)
	case errors.Is(err, store.ErrGameClosed):
		return status.Error(codes.FailedPrecondition, "game is not joinable")
	case errors.Is(err, store.ErrAlreadySeated):
		return status.Error(codes.AlreadyExists, "user is already at this table")
	case errors.Is(err, store.ErrTableFull):
		return status.Error(codes.ResourceExhausted, "table is full")
	case errors.Is(err, store.ErrUserNotAtTable):
		return status.Error(codes.NotFound, "user is not at this table")
	case errors.Is(err, store.ErrNotFound):
		return status.Error(codes.NotFound, "game or user not found")
	case errors.Is(err, store.ErrConcurrentWrite):
		return status.Error(codes.Unavailable, "concurrent write conflict; retry")
	default:
		log.Printf("lobby error: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func (s *Server) JoinWaitingList(_ context.Context, _ *pb.JoinWaitingListCommand) (*pb.JoinWaitingListResponse, error) {
	return nil, status.Error(codes.Unimplemented, "JoinWaitingList not implemented")
}

func (s *Server) LeaveWaitingList(_ context.Context, _ *pb.LeaveWaitingListCommand) (*pb.LeaveWaitingListResponse, error) {
	return nil, status.Error(codes.Unimplemented, "LeaveWaitingList not implemented")
}

func (s *Server) ChangeSeat(_ context.Context, _ *pb.ChangeSeatCommand) (*pb.ChangeSeatResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ChangeSeat not implemented")
}

func (s *Server) TopUp(_ context.Context, _ *pb.TopUpCommand) (*pb.TopUpResponse, error) {
	return nil, status.Error(codes.Unimplemented, "TopUp not implemented")
}

func (s *Server) EnableAutoRebuy(_ context.Context, _ *pb.EnableAutoRebuyCommand) (*pb.EnableAutoRebuyResponse, error) {
	return nil, status.Error(codes.Unimplemented, "EnableAutoRebuy not implemented")
}

func (s *Server) CashOut(_ context.Context, _ *pb.CashOutCommand) (*pb.CashOutResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CashOut not implemented")
}

func (s *Server) PauseTable(_ context.Context, _ *pb.PauseTableCommand) (*pb.PauseTableResponse, error) {
	return nil, status.Error(codes.Unimplemented, "PauseTable not implemented")
}

func (s *Server) ResumeTable(_ context.Context, _ *pb.ResumeTableCommand) (*pb.ResumeTableResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ResumeTable not implemented")
}

func (s *Server) CloseTable(_ context.Context, _ *pb.CloseTableCommand) (*pb.CloseTableResponse, error) {
	return nil, status.Error(codes.Unimplemented, "CloseTable not implemented")
}

func (s *Server) KickPlayer(_ context.Context, _ *pb.KickPlayerCommand) (*pb.KickPlayerResponse, error) {
	return nil, status.Error(codes.Unimplemented, "KickPlayer not implemented")
}

func (s *Server) MutePlayer(_ context.Context, _ *pb.MutePlayerCommand) (*pb.MutePlayerResponse, error) {
	return nil, status.Error(codes.Unimplemented, "MutePlayer not implemented")
}


func (s *Server) StreamGameEvents(_ *pb.StreamGameEventsRequest, _ pb.PokerService_StreamGameEventsServer) error {
	return status.Error(codes.Unimplemented, "StreamGameEvents not implemented")
}

func (s *Server) GetGameState(_ context.Context, _ *pb.GetGameStateRequest) (*pb.GetGameStateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetGameState not implemented")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// parseOrGenUUID parses s as a UUID, or returns a new random UUID if s is empty.
// Returns uuid.Nil only when s is non-empty but invalid.
func parseOrGenUUID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.New(), nil
	}
	return uuid.Parse(s)
}

func summaryToProto(s store.GameConfigSummary) *pb.TableSummary {
	return &pb.TableSummary{
		GameId:     s.GameID.String(),
		TableName:  s.TableName,
		Variant:    s.Variant,
		Structure:  s.Structure,
		Currency:   s.Currency,
		SmallBlind: s.SmallBlind,
		BigBlind:   s.BigBlind,
		MinBuyIn:   s.MinBuyIn,
		MaxBuyIn:   s.MaxBuyIn,
		MaxSeats:   s.MaxSeats,
		IsPrivate:  s.IsPrivate,
		CreatedAt:  timestampFromTime(s.CreatedAt),
	}
}