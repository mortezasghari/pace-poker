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
	opts SessionOptions,
) *Session {
	opts.withDefaults()

	sessCtx, cancel := context.WithCancel(parentCtx)

	s := &Session{
		gameID:      gameID,
		store:       st,
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
		case env := <-s.inbox:
			events, err := s.process(ctx, state, env.actor, env.cmd)
			if err == nil {
				state = applyEvents(state, events)
			}
			env.reply <- result{events: events, err: err}
		case replyCh := <-s.testStateCh:
			replyCh <- state
		}
	}
}

// process handles one command:
//  1. Check idempotency via command_id.
//  2. Validate and compute events.
//  3. Persist events via the store BEFORE updating in-memory state.
//
// A DB write failure returns an error without mutating state.
func (s *Session) process(ctx context.Context, state *pb.GameState, actor string, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	if cmd.GetCommandId() != "" {
		if existing, err := s.lookupByCommandID(ctx, cmd.GetCommandId()); err == nil && existing != nil {
			return existing, nil
		}
	}

	events := handleCommand(state, cmd, actor)

	// Tag every event with the command that caused it before persisting.
	for _, evt := range events {
		if evt.CausedByCommandId == "" {
			evt.CausedByCommandId = cmd.GetCommandId()
		}
	}

	if err := s.store.AppendEvents(ctx, events); err != nil {
		return nil, fmt.Errorf("persist events: %w", err)
	}

	return events, nil
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