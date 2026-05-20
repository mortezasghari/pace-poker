package engine

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRouter_InvalidateRemovesSession(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 1)

	r := NewRouter(ctx, fs, RouterOptions{})
	t.Cleanup(r.Close)

	// Trigger lazy load of a session by submitting a command.
	cmd := makeFoldCmd(uuid.New().String(), gameID)
	_, _ = r.Submit(ctx, gameID, "p1", cmd)

	if r.ActiveSessionCount() != 1 {
		t.Fatalf("expected 1 session before invalidate, got %d", r.ActiveSessionCount())
	}

	r.Invalidate(gameID)

	if r.ActiveSessionCount() != 0 {
		t.Errorf("expected 0 sessions after invalidate, got %d", r.ActiveSessionCount())
	}
}

func TestRouter_InvalidateClosesSession(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 1)

	r := NewRouter(ctx, fs, RouterOptions{})
	t.Cleanup(r.Close)

	// Get a reference to the session before invalidating it.
	cmd := makeFoldCmd(uuid.New().String(), gameID)
	_, _ = r.Submit(ctx, gameID, "p1", cmd)

	r.mu.RLock()
	sess := r.sessions[gameID]
	r.mu.RUnlock()

	r.Invalidate(gameID)

	select {
	case <-sess.closed:
		// good
	case <-time.After(2 * time.Second):
		t.Error("session was not closed within 2s after Invalidate")
	}
}

func TestRouter_NextCommandAfterInvalidateReloadsState(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	gameID := uuid.New()
	fs.setSnapshot(gameID, makeState(gameID), 1)

	r := NewRouter(ctx, fs, RouterOptions{})
	t.Cleanup(r.Close)

	// First command: load session (snapCallCount becomes 1).
	cmd := makeFoldCmd(uuid.New().String(), gameID)
	_, _ = r.Submit(ctx, gameID, "p1", cmd)

	countAfterFirst := fs.snapCallCount

	r.Invalidate(gameID)

	// Second command after invalidation: must reload from store (snapCallCount increments again).
	cmd2 := makeFoldCmd(uuid.New().String(), gameID)
	_, _ = r.Submit(ctx, gameID, "p1", cmd2)

	if fs.snapCallCount <= countAfterFirst {
		t.Errorf("expected store reload after invalidate: snapCallCount=%d, was %d before second command",
			fs.snapCallCount, countAfterFirst)
	}
}
