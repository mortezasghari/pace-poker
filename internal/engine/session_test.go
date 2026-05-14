package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func makeState(gameID uuid.UUID) *pb.GameState {
	return &pb.GameState{
		GameId:  gameID.String(),
		Version: 1,
		Status:  pb.GameStatus_GAME_STATUS_WAITING,
	}
}

func makeFoldCmd(cmdID string, gameID uuid.UUID) *pb.PlayerCommand {
	return &pb.PlayerCommand{
		CommandId: cmdID,
		GameId:    gameID.String(),
		Payload:   &pb.PlayerCommand_Fold{Fold: &pb.FoldCommand{}},
	}
}

// newTestSession creates a Session and registers a cleanup that closes it and
// asserts the goroutine exits within 1 second.
func newTestSession(t *testing.T, st *fakeStore, opts SessionOptions) (*Session, uuid.UUID) {
	t.Helper()
	gameID := uuid.New()
	s := NewSession(context.Background(), gameID, makeState(gameID), st, opts)
	t.Cleanup(func() {
		s.Close()
		select {
		case <-s.closed:
		case <-time.After(time.Second):
			t.Error("session goroutine did not exit within 1s")
		}
	})
	return s, gameID
}

// stateForTest retrieves the current state pointer from the run goroutine.
// Safe to call only when the session is idle (not processing a command).
func stateForTest(s *Session) *pb.GameState {
	ch := make(chan *pb.GameState, 1)
	s.testStateCh <- ch
	return <-ch
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestSession_SubmitProducesRejection(t *testing.T) {
	s, gameID := newTestSession(t, newFakeStore(), SessionOptions{})

	events, err := s.Submit(context.Background(), makeFoldCmd(uuid.New().String(), gameID))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	rejected, ok := events[0].Event.(*pb.GameEvent_CommandRejected)
	if !ok {
		t.Fatalf("expected CommandRejected, got %T", events[0].Event)
	}
	if rejected.CommandRejected.GetCode() != "UNIMPLEMENTED" {
		t.Errorf("code: got %q, want UNIMPLEMENTED", rejected.CommandRejected.GetCode())
	}
}

func TestSession_SerializesCommands(t *testing.T) {
	fs := newFakeStore()
	s, gameID := newTestSession(t, fs, SessionOptions{InboxSize: 100})

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Submit(context.Background(), makeFoldCmd(uuid.New().String(), gameID))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	fs.mu.Lock()
	count := fs.appendCallCount
	fs.mu.Unlock()
	if count != n {
		t.Errorf("AppendEvents called %d times, want %d", count, n)
	}
}

func TestSession_ClosedReturnsError(t *testing.T) {
	s, gameID := newTestSession(t, newFakeStore(), SessionOptions{})

	s.Close()
	select {
	case <-s.closed:
	case <-time.After(time.Second):
		t.Fatal("session did not close within 1s")
	}

	_, err := s.Submit(context.Background(), makeFoldCmd(uuid.New().String(), gameID))
	if !errors.Is(err, ErrSessionClosed) {
		t.Errorf("expected ErrSessionClosed, got: %v", err)
	}
}

func TestSession_IdleTimeoutExits(t *testing.T) {
	s, _ := newTestSession(t, newFakeStore(), SessionOptions{IdleAfter: 50 * time.Millisecond})

	select {
	case <-s.closed:
		// good
	case <-time.After(time.Second):
		t.Fatal("session did not exit after idle timeout")
	}
}

func TestSession_ContextCancelOnSubmit(t *testing.T) {
	fs := newFakeStore()
	// Block AppendEvents so the run loop stays busy processing the first command.
	blockCh := make(chan struct{})
	startedCh := make(chan struct{}, 1)
	fs.appendBlockCh = blockCh
	fs.appendStartedCh = startedCh

	s, gameID := newTestSession(t, fs, SessionOptions{})

	// Release blockCh before the session-close cleanup (LIFO order: registered
	// after newTestSession → runs before it).
	t.Cleanup(func() {
		select {
		case <-blockCh:
		default:
			close(blockCh)
		}
	})

	// Start a submit that will block inside AppendEvents.
	go s.Submit(context.Background(), makeFoldCmd(uuid.New().String(), gameID))

	// Wait until the run loop is inside AppendEvents (guaranteed signal).
	select {
	case <-startedCh:
	case <-time.After(time.Second):
		t.Fatal("run loop did not start processing")
	}

	// Submit with an already-cancelled context. The run loop is blocked so
	// the inbox is empty, but we cancel before enqueuing — ctx.Done() fires.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Submit(ctx, makeFoldCmd(uuid.New().String(), gameID))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestSession_IdempotentByCommandID(t *testing.T) {
	fs := newFakeStore()
	s, gameID := newTestSession(t, fs, SessionOptions{})

	cmdID := uuid.New().String()

	if _, err := s.Submit(context.Background(), makeFoldCmd(cmdID, gameID)); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if _, err := s.Submit(context.Background(), makeFoldCmd(cmdID, gameID)); err != nil {
		t.Fatalf("second Submit: %v", err)
	}

	fs.mu.Lock()
	count := fs.appendCallCount
	fs.mu.Unlock()
	if count != 1 {
		t.Errorf("AppendEvents called %d times, want 1 (idempotent)", count)
	}
}

func TestSession_PersistFailureDoesNotMutateState(t *testing.T) {
	fs := newFakeStore()
	persistErr := errors.New("simulated disk full")
	fs.appendErr = persistErr

	s, gameID := newTestSession(t, fs, SessionOptions{})

	// First submit: persist fails — error must surface.
	_, err := s.Submit(context.Background(), makeFoldCmd(uuid.New().String(), gameID))
	if !errors.Is(err, persistErr) {
		t.Fatalf("expected persist error, got: %v", err)
	}

	before := stateForTest(s)

	// Fix the store.
	fs.mu.Lock()
	fs.appendErr = nil
	fs.mu.Unlock()

	// Second submit must succeed.
	if _, err := s.Submit(context.Background(), makeFoldCmd(uuid.New().String(), gameID)); err != nil {
		t.Fatalf("second Submit after fixing store: %v", err)
	}

	after := stateForTest(s)

	// applyEvents is a stub — state pointer must be unchanged.
	if before != after {
		t.Error("state pointer changed: applyEvents stub must return the same pointer")
	}
}