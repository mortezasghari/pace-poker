package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgerrcode"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/store/pgstore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	// ErrConcurrentWrite is returned when an event insert violates the
	// UNIQUE (game_id, sequence) constraint — the OCC failure signal.
	ErrConcurrentWrite     = errors.New("store: concurrent write detected (sequence already exists)")
	ErrNotFound            = errors.New("store: not found")
	ErrInsufficientBalance = errors.New("store: insufficient chip balance")
	ErrDuplicateExternalID = errors.New("store: external_id already in use")

	// Lobby join/leave errors.
	ErrInsufficientFunds = errors.New("store: insufficient funds for buy-in")
	ErrSeatTaken         = errors.New("store: seat already taken")
	ErrBuyInOutOfRange   = errors.New("store: buy-in out of range")
	ErrGameClosed        = errors.New("store: game is not joinable")
	ErrAlreadySeated     = errors.New("store: user is already at this table")
	ErrTableFull         = errors.New("store: table is full (no seats available)")
	ErrUserNotAtTable    = errors.New("store: user is not seated at this table")
)

// ── Lobby input / result types ────────────────────────────────────────────────
// These live in the store package (no engine dependency) so that both the
// lobby package and server package can reference them without import cycles.

// JoinTableInput holds the parameters for an atomic join operation.
type JoinTableInput struct {
	CommandID uuid.UUID
	GameID    uuid.UUID
	UserID    uuid.UUID
	Seat      int32 // 0 = auto-select the lowest available seat
	BuyIn     int64
}

// JoinTableResult is returned on a successful join.
type JoinTableResult struct {
	GameState      *pb.GameState
	NewSequence    uint64
	UserNewBalance int64
}

// LeaveTableInput holds the parameters for an atomic leave operation.
type LeaveTableInput struct {
	CommandID uuid.UUID
	GameID    uuid.UUID
	UserID    uuid.UUID
}

// LeaveTableResult is returned on a successful leave.
type LeaveTableResult struct {
	GameState      *pb.GameState
	CashOutAmount  int64
	UserNewBalance int64
}

// GameConfigSummary is a lightweight projection of game_configs used for list/search.
type GameConfigSummary struct {
	GameID     uuid.UUID
	TableName  string
	Variant    pb.GameVariant
	Structure  pb.BettingStructure
	Currency   string
	SmallBlind int64
	BigBlind   int64
	MinBuyIn   int64
	MaxBuyIn   int64
	MaxSeats   int32
	IsPrivate  bool
	CreatedAt  time.Time
}

// SearchFilter holds optional lobby search parameters.
// Zero values mean "no filter": empty string, 0, UNSPECIFIED.
type SearchFilter struct {
	NameQuery string
	BigBlind  int64
	Variant   pb.GameVariant
	Structure pb.BettingStructure
	Limit     int32
	Offset    int32
}

// Store is the repository interface consumed by the game engine.
type Store interface {
	// Configs
	CreateGameConfig(ctx context.Context, cfg *pb.CashGameConfig) (uuid.UUID, error)
	GetGameConfig(ctx context.Context, gameID uuid.UUID) (*pb.CashGameConfig, error)
	ListActiveGameConfigs(ctx context.Context, limit, offset int32) ([]GameConfigSummary, error)
	SoftDeleteGameConfig(ctx context.Context, gameID uuid.UUID) error
	SearchGameConfigs(ctx context.Context, f SearchFilter) ([]GameConfigSummary, int64, error)

	// Table lifecycle (high-level: config + event + snapshot in one tx)
	CreateTable(ctx context.Context, cfg *pb.CashGameConfig, commandID uuid.UUID) (*pb.GameState, error)

	// Events
	AppendEvent(ctx context.Context, evt *pb.GameEvent) error
	AppendEvents(ctx context.Context, evts []*pb.GameEvent) error // single transaction
	GetEventsForGame(ctx context.Context, gameID uuid.UUID, fromSequence uint64, limit int32) ([]*pb.GameEvent, error)
	GetLatestSequence(ctx context.Context, gameID uuid.UUID) (uint64, error)
	FindEventByCommandID(ctx context.Context, commandID uuid.UUID) (*pb.GameEvent, error)

	// Snapshots
	CreateSnapshot(ctx context.Context, state *pb.GameState, handNumber int64) error
	GetLatestSnapshot(ctx context.Context, gameID uuid.UUID) (*pb.GameState, uint64, error)
	GetSnapshotAtOrBefore(ctx context.Context, gameID uuid.UUID, sequence uint64) (*pb.GameState, uint64, error)

	// Transactions
	WithTx(ctx context.Context, fn func(Store) error) error

	// ── Users ─────────────────────────────────────────────────────────────────

	CreateUser(ctx context.Context, in UserInput) (User, UserSnapshot, error)
	GetUser(ctx context.Context, userID uuid.UUID) (User, UserSnapshot, error)
	GetUserByExternalID(ctx context.Context, externalID string) (User, UserSnapshot, error)
	UpdateUserSettings(ctx context.Context, in UpdateUserSettingsInput) (User, error)

	// ── Deposits ──────────────────────────────────────────────────────────────

	ReportSteps(ctx context.Context, in ReportStepsInput) (ReportStepsResult, error)
	ListDepositReports(ctx context.Context, userID uuid.UUID, limit, offset int32) ([]DepositReport, int64, error)
	FindDepositReport(ctx context.Context, userID, reportID uuid.UUID) (*DepositReport, error)

	// ── Balance (game integration) ────────────────────────────────────────────

	// DebitUserBalance atomically deducts amount from the user's chip balance.
	// Returns ErrInsufficientBalance if balance < amount.
	DebitUserBalance(ctx context.Context, userID uuid.UUID, amount int64) (UserSnapshot, error)
	// CreditUserBalance adds chips (e.g. on cash-out when leaving a table).
	CreditUserBalance(ctx context.Context, userID uuid.UUID, amount int64) (UserSnapshot, error)

	// GetUserSnapshot reads the current denormalized balance projection.
	GetUserSnapshot(ctx context.Context, userID uuid.UUID) (UserSnapshot, error)
}

type pgStore struct {
	pool    *pgxpool.Pool
	queries *pgstore.Queries
}

// New returns a Store backed by pool.
func New(pool *pgxpool.Pool) Store {
	return &pgStore{pool: pool, queries: pgstore.New(pool)}
}

// ── Configs ───────────────────────────────────────────────────────────────────

func (s *pgStore) CreateGameConfig(ctx context.Context, cfg *pb.CashGameConfig) (uuid.UUID, error) {
	b, err := proto.Marshal(cfg)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.queries.CreateGameConfig(ctx, pgstore.CreateGameConfigParams{
		TableName:   cfg.GetTableName(),
		Variant:     int16(cfg.GetVariant()),
		Structure:   int16(cfg.GetStructure()),
		Currency:    cfg.GetCurrency(),
		SmallBlind:  cfg.GetSmallBlind(),
		BigBlind:    cfg.GetBigBlind(),
		MinBuyIn:    cfg.GetMinBuyIn(),
		MaxBuyIn:    cfg.GetMaxBuyIn(),
		MaxSeats:    int16(cfg.GetMaxSeats()),
		IsPrivate:   cfg.GetIsPrivate(),
		ConfigProto: b,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert game_config: %w", err)
	}
	return row.GameID, nil
}

func (s *pgStore) GetGameConfig(ctx context.Context, gameID uuid.UUID) (*pb.CashGameConfig, error) {
	row, err := s.queries.GetGameConfig(ctx, gameID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get game_config: %w", err)
	}
	var cfg pb.CashGameConfig
	if err := proto.Unmarshal(row.ConfigProto, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

func (s *pgStore) ListActiveGameConfigs(ctx context.Context, limit, offset int32) ([]GameConfigSummary, error) {
	rows, err := s.queries.ListActiveGameConfigs(ctx, pgstore.ListActiveGameConfigsParams{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list game_configs: %w", err)
	}
	out := make([]GameConfigSummary, len(rows))
	for i, r := range rows {
		out[i] = rowToSummary(pgstore.SearchGameConfigsRow{
			GameID:     r.GameID,
			TableName:  r.TableName,
			Variant:    r.Variant,
			Structure:  r.Structure,
			Currency:   r.Currency,
			SmallBlind: r.SmallBlind,
			BigBlind:   r.BigBlind,
			MinBuyIn:   r.MinBuyIn,
			MaxBuyIn:   r.MaxBuyIn,
			MaxSeats:   r.MaxSeats,
			IsPrivate:  r.IsPrivate,
			CreatedAt:  r.CreatedAt,
		})
	}
	return out, nil
}

func (s *pgStore) SoftDeleteGameConfig(ctx context.Context, gameID uuid.UUID) error {
	if err := s.queries.SoftDeleteGameConfig(ctx, gameID); err != nil {
		return fmt.Errorf("soft-delete game_config: %w", err)
	}
	return nil
}

// SearchGameConfigs runs the search query and count query in one transaction,
// returning matching rows and the total count before pagination.
func (s *pgStore) SearchGameConfigs(ctx context.Context, f SearchFilter) ([]GameConfigSummary, int64, error) {
	params := pgstore.SearchGameConfigsParams{
		Column1: f.NameQuery,
		Column2: f.BigBlind,
		Column3: int16(f.Variant),
		Column4: int16(f.Structure),
		Limit:   f.Limit,
		Offset:  f.Offset,
	}
	countParams := pgstore.CountSearchGameConfigsParams{
		Column1: f.NameQuery,
		Column2: f.BigBlind,
		Column3: int16(f.Variant),
		Column4: int16(f.Structure),
	}

	var (
		rows  []pgstore.SearchGameConfigsRow
		total int64
	)
	err := s.WithTx(ctx, func(tx Store) error {
		ps := tx.(*pgStore)
		var err error
		rows, err = ps.queries.SearchGameConfigs(ctx, params)
		if err != nil {
			return fmt.Errorf("search game_configs: %w", err)
		}
		total, err = ps.queries.CountSearchGameConfigs(ctx, countParams)
		if err != nil {
			return fmt.Errorf("count game_configs: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	out := make([]GameConfigSummary, len(rows))
	for i, r := range rows {
		out[i] = rowToSummary(r)
	}
	return out, total, nil
}

// CreateTable atomically: inserts config, appends TableCreated event at seq=1,
// creates initial snapshot at seq=1. Returns the initial GameState.
func (s *pgStore) CreateTable(ctx context.Context, cfg *pb.CashGameConfig, commandID uuid.UUID) (*pb.GameState, error) {
	var state *pb.GameState

	err := s.WithTx(ctx, func(tx Store) error {
		gameID, err := tx.CreateGameConfig(ctx, cfg)
		if err != nil {
			return err
		}

		now := timestamppb.Now()
		state = &pb.GameState{
			GameId:     gameID.String(),
			Status:     pb.GameStatus_GAME_STATUS_WAITING,
			Version:    1,
			Config:     cfg,
			DealerSeat: -1,
			HandNumber: 0,
		}

		var cmdIDStr string
		if commandID != uuid.Nil {
			cmdIDStr = commandID.String()
		}

		evt := &pb.GameEvent{
			GameId:            gameID.String(),
			Sequence:          1,
			OccurredAt:        now,
			CausedByCommandId: cmdIDStr,
			StateVersion:      1,
			Event: &pb.GameEvent_TableCreated{
				TableCreated: &pb.TableCreated{Config: cfg},
			},
		}
		if err := tx.AppendEvent(ctx, evt); err != nil {
			return err
		}

		return tx.CreateSnapshot(ctx, state, 0)
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

// ── Events ────────────────────────────────────────────────────────────────────

func (s *pgStore) AppendEvent(ctx context.Context, evt *pb.GameEvent) error {
	p, err := eventToParams(evt)
	if err != nil {
		return err
	}
	_, err = s.queries.AppendGameEvent(ctx, pgstore.AppendGameEventParams{
		GameID:            p.GameID,
		Sequence:          p.Sequence,
		EventType:         p.EventType,
		Payload:           p.Payload,
		Envelope:          p.Envelope,
		CausedByCommandID: p.CausedByCommandID,
		HandID:            p.HandID,
		StateVersionAfter: p.StateVersionAfter,
		OccurredAt:        p.OccurredAt,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConcurrentWrite
		}
		return fmt.Errorf("insert game_event: %w", err)
	}
	return nil
}

// AppendEvents inserts multiple events atomically using pgx COPY FROM.
// A unique-sequence violation on any row rolls back all rows.
func (s *pgStore) AppendEvents(ctx context.Context, evts []*pb.GameEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	params := make([]pgstore.AppendGameEventsParams, len(evts))
	for i, evt := range evts {
		p, err := eventToParams(evt)
		if err != nil {
			return err
		}
		params[i] = pgstore.AppendGameEventsParams{
			GameID:            p.GameID,
			Sequence:          p.Sequence,
			EventType:         p.EventType,
			Payload:           p.Payload,
			Envelope:          p.Envelope,
			CausedByCommandID: p.CausedByCommandID,
			HandID:            p.HandID,
			StateVersionAfter: p.StateVersionAfter,
			OccurredAt:        p.OccurredAt,
		}
	}
	if _, err := pgstore.New(tx).AppendGameEvents(ctx, params); err != nil {
		if isUniqueViolation(err) {
			return ErrConcurrentWrite
		}
		return fmt.Errorf("bulk insert game_events: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *pgStore) GetEventsForGame(ctx context.Context, gameID uuid.UUID, fromSequence uint64, limit int32) ([]*pb.GameEvent, error) {
	rows, err := s.queries.GetEventsForGame(ctx, pgstore.GetEventsForGameParams{
		GameID:   gameID,
		Sequence: int64(fromSequence),
		Limit:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	return unmarshalEventRows(rows)
}

func (s *pgStore) GetLatestSequence(ctx context.Context, gameID uuid.UUID) (uint64, error) {
	seq, err := s.queries.GetLatestSequence(ctx, gameID)
	if err != nil {
		return 0, fmt.Errorf("get latest sequence: %w", err)
	}
	return uint64(seq), nil
}

func (s *pgStore) FindEventByCommandID(ctx context.Context, commandID uuid.UUID) (*pb.GameEvent, error) {
	row, err := s.queries.FindEventByCommandID(ctx, uuid.NullUUID{UUID: commandID, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find event by command: %w", err)
	}
	var evt pb.GameEvent
	if err := proto.Unmarshal(row.Envelope, &evt); err != nil {
		return nil, fmt.Errorf("unmarshal event: %w", err)
	}
	return &evt, nil
}

// ── Snapshots ─────────────────────────────────────────────────────────────────

func (s *pgStore) CreateSnapshot(ctx context.Context, state *pb.GameState, handNumber int64) error {
	gameID, err := uuid.Parse(state.GetGameId())
	if err != nil {
		return fmt.Errorf("parse game_id: %w", err)
	}
	b, err := proto.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	_, err = s.queries.CreateSnapshot(ctx, pgstore.CreateSnapshotParams{
		GameID:     gameID,
		Sequence:   state.GetVersion(),
		HandNumber: handNumber,
		StateProto: b,
	})
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}
	return nil
}

func (s *pgStore) GetLatestSnapshot(ctx context.Context, gameID uuid.UUID) (*pb.GameState, uint64, error) {
	row, err := s.queries.GetLatestSnapshot(ctx, gameID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("get latest snapshot: %w", err)
	}
	return unmarshalSnapshot(row)
}

func (s *pgStore) GetSnapshotAtOrBefore(ctx context.Context, gameID uuid.UUID, sequence uint64) (*pb.GameState, uint64, error) {
	row, err := s.queries.GetSnapshotAtOrBefore(ctx, pgstore.GetSnapshotAtOrBeforeParams{
		GameID:   gameID,
		Sequence: int64(sequence),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("get snapshot at/before %d: %w", sequence, err)
	}
	return unmarshalSnapshot(row)
}

// ── Transactions ──────────────────────────────────────────────────────────────

func (s *pgStore) WithTx(ctx context.Context, fn func(Store) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txStore := &pgStore{pool: s.pool, queries: pgstore.New(tx)}
	if err := fn(txStore); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func rowToSummary(r pgstore.SearchGameConfigsRow) GameConfigSummary {
	return GameConfigSummary{
		GameID:     r.GameID,
		TableName:  r.TableName,
		Variant:    pb.GameVariant(r.Variant),
		Structure:  pb.BettingStructure(r.Structure),
		Currency:   r.Currency,
		SmallBlind: r.SmallBlind,
		BigBlind:   r.BigBlind,
		MinBuyIn:   r.MinBuyIn,
		MaxBuyIn:   r.MaxBuyIn,
		MaxSeats:   int32(r.MaxSeats),
		IsPrivate:  r.IsPrivate,
		CreatedAt:  r.CreatedAt.Time,
	}
}

type eventParams struct {
	GameID            uuid.UUID
	Sequence          int64
	EventType         string
	Payload           []byte
	Envelope          []byte
	CausedByCommandID uuid.NullUUID
	HandID            uuid.NullUUID
	StateVersionAfter int64
	OccurredAt        pgtype.Timestamptz
}

func eventToParams(evt *pb.GameEvent) (*eventParams, error) {
	gameID, err := uuid.Parse(evt.GetGameId())
	if err != nil {
		return nil, fmt.Errorf("parse game_id %q: %w", evt.GetGameId(), err)
	}

	eventType, payload, err := extractInnerEvent(evt)
	if err != nil {
		return nil, err
	}

	envelope, err := proto.Marshal(evt)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	var cmdID uuid.NullUUID
	if s := evt.GetCausedByCommandId(); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("parse command_id %q: %w", s, err)
		}
		cmdID = uuid.NullUUID{UUID: id, Valid: true}
	}

	return &eventParams{
		GameID:            gameID,
		Sequence:          int64(evt.GetSequence()),
		EventType:         eventType,
		Payload:           payload,
		Envelope:          envelope,
		CausedByCommandID: cmdID,
		HandID:            extractHandID(evt),
		StateVersionAfter: evt.GetStateVersion(),
		OccurredAt:        pgtype.Timestamptz{Time: evt.GetOccurredAt().AsTime(), Valid: true},
	}, nil
}

// extractInnerEvent returns the fully-qualified proto message name and marshaled
// bytes of the variant message set in the GameEvent oneof.
func extractInnerEvent(evt *pb.GameEvent) (eventType string, payload []byte, err error) {
	r := evt.ProtoReflect()
	oneofDesc := r.Descriptor().Oneofs().ByName("event")
	if oneofDesc == nil {
		return "", nil, errors.New("GameEvent: missing 'event' oneof descriptor")
	}
	field := r.WhichOneof(oneofDesc)
	if field == nil {
		return "", nil, errors.New("GameEvent: 'event' oneof is not set")
	}
	inner := r.Get(field).Message().Interface()
	eventType = string(proto.MessageName(inner))
	payload, err = proto.Marshal(inner)
	if err != nil {
		err = fmt.Errorf("marshal inner event %s: %w", eventType, err)
	}
	return
}

// extractHandID pulls a hand_id from events that carry one.
// Events without a hand_id return an invalid NullUUID, stored as NULL.
func extractHandID(evt *pb.GameEvent) uuid.NullUUID {
	if hs := evt.GetHandStarted(); hs != nil {
		if id, err := uuid.Parse(hs.GetHandId()); err == nil {
			return uuid.NullUUID{UUID: id, Valid: true}
		}
	}
	return uuid.NullUUID{}
}

func unmarshalEventRows(rows []pgstore.GameEvent) ([]*pb.GameEvent, error) {
	out := make([]*pb.GameEvent, len(rows))
	for i, row := range rows {
		var evt pb.GameEvent
		if err := proto.Unmarshal(row.Envelope, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal event seq=%d: %w", row.Sequence, err)
		}
		out[i] = &evt
	}
	return out, nil
}

func unmarshalSnapshot(row pgstore.GameSnapshot) (*pb.GameState, uint64, error) {
	var state pb.GameState
	if err := proto.Unmarshal(row.StateProto, &state); err != nil {
		return nil, 0, fmt.Errorf("unmarshal snapshot seq=%d: %w", row.Sequence, err)
	}
	return &state, uint64(row.Sequence), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}