package engine

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrSessionClosed = errors.New("engine: session is closed")
	ErrInboxFull     = errors.New("engine: session inbox is full")
)

// SessionOptions configures a new Session.
type SessionOptions struct {
	InboxSize int           // default 64
	IdleAfter time.Duration // default 10 minutes
}

func (o *SessionOptions) withDefaults() {
	if o.InboxSize == 0 {
		o.InboxSize = 64
	}
	if o.IdleAfter == 0 {
		o.IdleAfter = 10 * time.Minute
	}
}

// Session is the in-memory owner of one game's state.
//
// Concurrency model:
//   - The state field is accessed ONLY by the run goroutine.
//   - External callers communicate via Submit, which sends on inbox.
//   - This is the classic Go actor pattern: a goroutine + a channel = serialized access.
//
// Lifecycle:
//   - NewSession starts the run goroutine immediately.
//   - Close signals the goroutine to exit. Submit calls after Close return ErrSessionClosed.
//   - The run goroutine exits when ctx is cancelled, when Close is called, or when the
//     idle timeout elapses with no incoming commands.
type Session struct {
	gameID    uuid.UUID
	store     store.Store
	dealer    Dealer
	inbox     chan envelope
	closed    chan struct{} // closed when run() returns
	closeOnce func()       // idempotent close trigger
	lastUsed  atomic.Int64 // unix nanos; updated on every Submit
	idleAfter time.Duration

	// testStateCh is used exclusively by tests to read the current state pointer
	// from the run goroutine without a data race. It is always initialized;
	// since no one sends on it in production, the select case never fires.
	testStateCh chan chan *pb.GameState
}

type envelope struct {
	cmd   *pb.PlayerCommand
	actor string
	reply chan result
}

type result struct {
	events []*pb.GameEvent
	err    error
}

// NewSession constructs a Session and starts its run goroutine.
// The caller is responsible for loading initial state from the store before
// calling NewSession — typically inside Router.getOrLoad.
func NewSession(
	parentCtx context.Context,
	gameID uuid.UUID,
	initialState *pb.GameState,
	st store.Store,
	dlr Dealer,
	opts SessionOptions,
) *Session {
	opts.withDefaults()

	sessCtx, cancel := context.WithCancel(parentCtx)

	s := &Session{
		gameID:      gameID,
		store:       st,
		dealer:      dlr,
		inbox:       make(chan envelope, opts.InboxSize),
		closed:      make(chan struct{}),
		idleAfter:   opts.IdleAfter,
		testStateCh: make(chan chan *pb.GameState),
	}
	s.lastUsed.Store(time.Now().UnixNano())

	var closeCalled atomic.Bool
	s.closeOnce = func() {
		if closeCalled.CompareAndSwap(false, true) {
			cancel()
		}
	}

	go s.run(sessCtx, initialState)
	return s
}

// Submit sends a command to the session and waits for the resulting events.
// actorPlayerID identifies who is sending the command (verified by the caller's
// auth layer, not trusted from the proto payload).
// Safe to call from many goroutines concurrently — the session serializes them.
func (s *Session) Submit(ctx context.Context, actorPlayerID string, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	s.lastUsed.Store(time.Now().UnixNano())

	reply := make(chan result, 1) // buffered so run never blocks on a giving-up caller
	env := envelope{cmd: cmd, actor: actorPlayerID, reply: reply}

	select {
	case s.inbox <- env:
	case <-s.closed:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case r := <-reply:
		return r.events, r.err
	case <-s.closed:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down the session. Idempotent.
func (s *Session) Close() { s.closeOnce() }

// Wait blocks until the session's run goroutine has exited.
func (s *Session) Wait() { <-s.closed }

// GameID returns the game this session manages.
func (s *Session) GameID() uuid.UUID { return s.gameID }

// IdleFor returns how long it has been since the last Submit.
func (s *Session) IdleFor() time.Duration {
	last := time.Unix(0, s.lastUsed.Load())
	return time.Since(last)
}

// run is the actor loop. ONLY this goroutine may touch state.
// Exits when: ctx is cancelled, or the idle timer fires.
func (s *Session) run(ctx context.Context, state *pb.GameState) {
	defer close(s.closed)

	idle := time.NewTimer(s.idleAfter)
	defer idle.Stop()

	// dlTimer fires when the acting player's action deadline expires so the
	// session can advance proactively (emit PlayerTimedOut + default action)
	// without waiting for the next incoming command.
	var (
		dlTimer *time.Timer
		dlC     <-chan time.Time // nil when no active deadline
	)
	defer func() {
		if dlTimer != nil {
			dlTimer.Stop()
		}
	}()

	// resetDeadline arms or disarms dlTimer based on the current state.
	// Must be called after every state mutation.
	resetDeadline := func() {
		if dlTimer != nil {
			if !dlTimer.Stop() {
				select {
				case <-dlTimer.C:
				default:
				}
			}
			dlTimer, dlC = nil, nil
		}
		h := state.CurrentHand
		if h == nil || h.ActingPlayerId == nil || h.ActionDeadline == nil {
			return
		}
		d := time.Until(h.ActionDeadline.AsTime())
		if d <= 0 {
			// Already expired; the lazy check in advance() handles it on
			// the next command. No proactive timer needed.
			return
		}
		dlTimer = time.NewTimer(d)
		dlC = dlTimer.C
	}

	// Arm deadline timer for the initial state (session may be loaded from
	// the store while a hand is already in progress).
	resetDeadline()

	for {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(s.idleAfter)

		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			return
		case <-dlC:
			// Deadline expired: advance proactively to emit PlayerTimedOut +
			// default action and persist the results.
			dlTimer, dlC = nil, nil
			events, err := s.runProactiveAdvance(ctx, state)
			if err == nil && len(events) > 0 {
				state = applyAll(state, events)
			}
			resetDeadline()
		case env := <-s.inbox:
			events, err := s.process(ctx, state, env.actor, env.cmd)
			if err == nil {
				state = applyAll(state, events)
			}
			env.reply <- result{events: events, err: err}
			resetDeadline()
		case replyCh := <-s.testStateCh:
			replyCh <- state
		}
	}
}

// runProactiveAdvance drives the lifecycle forward without a client command
// (e.g. after an action deadline fires). Events are stamped and persisted
// exactly like command-driven events. CausedByCommandId is left empty, which
// is correct for server-initiated events per the proto definition.
func (s *Session) runProactiveAdvance(ctx context.Context, state *pb.GameState) ([]*pb.GameEvent, error) {
	events, err := runAdvance(state, s.dealer)
	if err != nil || len(events) == 0 {
		return nil, err
	}
	if err := s.stampEvents(ctx, events); err != nil {
		return nil, fmt.Errorf("stamp proactive advance: %w", err)
	}
	if err := s.store.AppendEvents(ctx, events); err != nil {
		return nil, fmt.Errorf("persist proactive advance: %w", err)
	}
	return events, nil
}

// process handles one command:
//  1. Check idempotency via command_id.
//  2. Intercept session-layer commands (Resume) before engine dispatch.
//  3. Validate and compute events via the engine.
//  4. Persist events via the store BEFORE updating in-memory state.
//
// A DB write failure returns an error without mutating state.
func (s *Session) process(ctx context.Context, state *pb.GameState, actor string, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	if cmd.GetCommandId() != "" {
		if existing, err := s.lookupByCommandID(ctx, cmd.GetCommandId()); err == nil && existing != nil {
			return existing, nil
		}
	}

	// Resume is handled here so it can check exactly PAUSED status and then
	// run advance to start a new hand if needed — without engine validation baggage.
	if _, ok := cmd.Payload.(*pb.PlayerCommand_Resume); ok {
		return s.handleResume(ctx, state, cmd)
	}

	events, err := HandleCommand(state, cmd, actor, s.dealer)
	if err != nil {
		return nil, fmt.Errorf("handle command: %w", err)
	}

	// Tag every event with the command that caused it before persisting.
	for _, evt := range events {
		if evt.CausedByCommandId == "" {
			evt.CausedByCommandId = cmd.GetCommandId()
		}
	}

	if err := s.stampEvents(ctx, events); err != nil {
		return nil, fmt.Errorf("stamp events: %w", err)
	}

	if err := s.store.AppendEvents(ctx, events); err != nil {
		return nil, fmt.Errorf("persist events: %w", err)
	}

	return events, nil
}

// handleResume processes a ResumeCommand at the session layer.
// Only valid when the game is PAUSED; runs advance after resuming so the
// engine can start a new hand immediately if conditions are met.
func (s *Session) handleResume(ctx context.Context, state *pb.GameState, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	cmdID := cmd.GetCommandId()
	tag := func(evts []*pb.GameEvent) {
		for _, e := range evts {
			if e.CausedByCommandId == "" {
				e.CausedByCommandId = cmdID
			}
		}
	}

	if state.Status != pb.GameStatus_GAME_STATUS_PAUSED {
		reason := reject(CodeIllegalAction, "cannot resume: game is not paused (status=%s)", state.Status)
		evt := reason.toCommandRejectedEvent(state.GetGameId(), cmdID)
		tag([]*pb.GameEvent{evt})
		if err := s.stampEvents(ctx, []*pb.GameEvent{evt}); err != nil {
			return nil, fmt.Errorf("stamp resume rejection: %w", err)
		}
		if err := s.store.AppendEvents(ctx, []*pb.GameEvent{evt}); err != nil {
			return nil, fmt.Errorf("persist resume rejection: %w", err)
		}
		return []*pb.GameEvent{evt}, nil
	}

	resumeEvt := &pb.GameEvent{Event: &pb.GameEvent_TableResumed{TableResumed: &pb.TableResumed{}}}

	// Apply resume, then drive the lifecycle forward.
	curr := Apply(state, resumeEvt)
	advEvts, err := runAdvance(curr, s.dealer)
	if err != nil {
		return nil, fmt.Errorf("advance after resume: %w", err)
	}

	allEvts := append([]*pb.GameEvent{resumeEvt}, advEvts...)
	tag(allEvts)

	if err := s.stampEvents(ctx, allEvts); err != nil {
		return nil, fmt.Errorf("stamp resume events: %w", err)
	}

	if err := s.store.AppendEvents(ctx, allEvts); err != nil {
		return nil, fmt.Errorf("persist resume events: %w", err)
	}
	return allEvts, nil
}

// stampEvents sets GameId, Sequence, StateVersion, and OccurredAt on every
// event in the slice, fetching the current latest sequence from the store to
// assign the next monotonically increasing values. Must be called immediately
// before AppendEvents — the gap between GetLatestSequence and the insert is
// safe because the session serialises all writes for a given game.
func (s *Session) stampEvents(ctx context.Context, events []*pb.GameEvent) error {
	if len(events) == 0 {
		return nil
	}
	latestSeq, err := s.store.GetLatestSequence(ctx, s.gameID)
	if err != nil {
		return fmt.Errorf("get latest sequence: %w", err)
	}
	gameID := s.gameID.String()
	now := timestamppb.Now()
	for i, evt := range events {
		evt.GameId = gameID
		seq := latestSeq + uint64(i) + 1
		evt.Sequence = seq
		evt.StateVersion = int64(seq)
		if evt.OccurredAt == nil {
			evt.OccurredAt = now
		}
	}
	return nil
}

func (s *Session) lookupByCommandID(ctx context.Context, commandID string) ([]*pb.GameEvent, error) {
	id, err := uuid.Parse(commandID)
	if err != nil {
		return nil, nil // malformed → treat as unseen, let handler reject
	}
	evt, err := s.store.FindEventByCommandID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []*pb.GameEvent{evt}, nil
}