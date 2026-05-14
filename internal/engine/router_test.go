package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestRouter(t *testing.T, fs *fakeStore, opts RouterOptions) *Router {
	t.Helper()
	r := NewRouter(context.Background(), fs, opts)
	t.Cleanup(r.Close)
	return r
}

func TestRouter_LazyLoadsSession(t *testing.T) {
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 0)

	r := newTestRouter(t, fs, RouterOptions{})

	if r.ActiveSessionCount() != 0 {
		t.Fatalf("expected 0 sessions before first submit, got %d", r.ActiveSessionCount())
	}

	_, err := r.Submit(context.Background(), gameID, makeFoldCmd(uuid.New().String(), gameID))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if r.ActiveSessionCount() != 1 {
		t.Errorf("expected 1 session after submit, got %d", r.ActiveSessionCount())
	}
}

func TestRouter_ReturnsErrGameNotFound(t *testing.T) {
	fs := newFakeStore() // no snapshots registered
	r := newTestRouter(t, fs, RouterOptions{})

	_, err := r.Submit(context.Background(), uuid.New(), makeFoldCmd(uuid.New().String(), uuid.New()))
	if !errors.Is(err, ErrGameNotFound) {
		t.Errorf("expected ErrGameNotFound, got: %v", err)
	}
}

func TestRouter_ReusesExistingSession(t *testing.T) {
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 0)

	r := newTestRouter(t, fs, RouterOptions{})

	cmd := func() { r.Submit(context.Background(), gameID, makeFoldCmd(uuid.New().String(), gameID)) } //nolint:errcheck
	cmd()
	cmd()

	if r.ActiveSessionCount() != 1 {
		t.Errorf("expected 1 session after 2 submits, got %d", r.ActiveSessionCount())
	}

	fs.mu.Lock()
	snapCalls := fs.snapCallCount
	fs.mu.Unlock()
	if snapCalls != 1 {
		t.Errorf("GetLatestSnapshot called %d times, want 1", snapCalls)
	}
}

func TestRouter_ConcurrentLoadOfSameGame(t *testing.T) {
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 0)

	r := newTestRouter(t, fs, RouterOptions{})

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.Submit(context.Background(), gameID, makeFoldCmd(uuid.New().String(), gameID))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	if r.ActiveSessionCount() != 1 {
		t.Errorf("expected 1 session, got %d", r.ActiveSessionCount())
	}

	fs.mu.Lock()
	snapCalls := fs.snapCallCount
	fs.mu.Unlock()
	if snapCalls != 1 {
		t.Errorf("GetLatestSnapshot called %d times, want 1 (double-checked locking)", snapCalls)
	}
}

func TestRouter_ReapsExitedSessions(t *testing.T) {
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 0)

	r := newTestRouter(t, fs, RouterOptions{
		SessionOptions: SessionOptions{IdleAfter: 50 * time.Millisecond},
		ReapInterval:   time.Hour, // disable automatic reaping; we call reapOnce manually
	})

	if _, err := r.Submit(context.Background(), gameID, makeFoldCmd(uuid.New().String(), gameID)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if r.ActiveSessionCount() != 1 {
		t.Fatalf("expected 1 session after submit, got %d", r.ActiveSessionCount())
	}

	// Retrieve the session pointer while it's still in the map.
	r.mu.RLock()
	sess := r.sessions[gameID]
	r.mu.RUnlock()

	// Wait for the idle timeout to fire.
	select {
	case <-sess.closed:
	case <-time.After(time.Second):
		t.Fatal("session did not exit after idle timeout")
	}

	// Before reap: session is still in the map (reaper hasn't run).
	if r.ActiveSessionCount() != 1 {
		t.Errorf("expected 1 session before reap, got %d", r.ActiveSessionCount())
	}

	r.reapOnce()

	if r.ActiveSessionCount() != 0 {
		t.Errorf("expected 0 sessions after reap, got %d", r.ActiveSessionCount())
	}
}

func TestRouter_CloseShutsDownAllSessions(t *testing.T) {
	fs := newFakeStore()

	// Use a long idle timeout so sessions don't exit on their own during the test.
	r := NewRouter(context.Background(), fs, RouterOptions{
		SessionOptions: SessionOptions{IdleAfter: time.Hour},
	})

	const n = 5
	gameIDs := make([]uuid.UUID, n)
	for i := range n {
		id := uuid.New()
		gameIDs[i] = id
		fs.setSnapshot(id, makeState(id), 0)
		if _, err := r.Submit(context.Background(), id, makeFoldCmd(uuid.New().String(), id)); err != nil {
			t.Fatalf("Submit game %d: %v", i, err)
		}
	}
	if r.ActiveSessionCount() != n {
		t.Fatalf("expected %d sessions, got %d", n, r.ActiveSessionCount())
	}

	// Collect session pointers before Close clears the map.
	sessions := make([]*Session, n)
	r.mu.RLock()
	for i, id := range gameIDs {
		sessions[i] = r.sessions[id]
	}
	r.mu.RUnlock()

	r.Close()

	for i, s := range sessions {
		select {
		case <-s.closed:
		case <-time.After(time.Second):
			t.Errorf("session %d did not close within 1s", i)
		}
	}
	if r.ActiveSessionCount() != 0 {
		t.Errorf("expected 0 sessions after Close, got %d", r.ActiveSessionCount())
	}
}