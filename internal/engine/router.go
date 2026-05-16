package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/store"
)

// ErrGameNotFound is returned when a command targets a game that has no
// snapshot in the store (i.e. the game_id was never created).
var ErrGameNotFound = errors.New("engine: game not found")

// RouterOptions configures a Router.
type RouterOptions struct {
	SessionOptions SessionOptions
	ReapInterval   time.Duration    // default 1 minute
	DealerFactory  func() Dealer    // default: NewCryptoDealer
}

func (o *RouterOptions) withDefaults() {
	o.SessionOptions.withDefaults()
	if o.ReapInterval == 0 {
		o.ReapInterval = time.Minute
	}
	if o.DealerFactory == nil {
		o.DealerFactory = func() Dealer { return NewCryptoDealer() }
	}
}

// Router routes commands to the correct Session, creating sessions on demand.
//
// Concurrency:
//   - The sessions map is protected by mu. The mutex is held only around map
//     operations, never during command processing.
//   - Each Session has its own goroutine; the Router never blocks one game on another.
type Router struct {
	store    store.Store
	opts     RouterOptions
	mu       sync.RWMutex
	sessions map[uuid.UUID]*Session
	ctx      context.Context
	cancel   context.CancelFunc
	reaperWG sync.WaitGroup
}

// NewRouter creates a Router and starts the background reaper.
func NewRouter(parentCtx context.Context, st store.Store, opts RouterOptions) *Router {
	opts.withDefaults()
	ctx, cancel := context.WithCancel(parentCtx)
	r := &Router{
		store:    st,
		opts:     opts,
		sessions: make(map[uuid.UUID]*Session),
		ctx:      ctx,
		cancel:   cancel,
	}
	r.reaperWG.Add(1)
	go r.reapLoop()
	return r
}

// Submit routes a command to the session for gameID, loading it if necessary.
// actorPlayerID is the verified identity of the caller (from auth, not the proto).
func (r *Router) Submit(ctx context.Context, gameID uuid.UUID, actorPlayerID string, cmd *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	sess, err := r.getOrLoad(ctx, gameID)
	if err != nil {
		return nil, err
	}
	return sess.Submit(ctx, actorPlayerID, cmd)
}

// getOrLoad returns the session for gameID, loading from the store if needed.
func (r *Router) getOrLoad(ctx context.Context, gameID uuid.UUID) (*Session, error) {
	// Fast path: session already in memory.
	r.mu.RLock()
	sess, ok := r.sessions[gameID]
	r.mu.RUnlock()
	if ok {
		return sess, nil
	}

	// Slow path: load from store under write lock.
	// Double-check after acquiring write lock — another goroutine may have
	// loaded the same session while we waited.
	r.mu.Lock()
	if sess, ok := r.sessions[gameID]; ok {
		r.mu.Unlock()
		return sess, nil
	}

	state, err := r.loadState(ctx, gameID)
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}

	sess = NewSession(r.ctx, gameID, state, r.store, r.opts.DealerFactory(), r.opts.SessionOptions)
	r.sessions[gameID] = sess
	r.mu.Unlock()
	return sess, nil
}

// loadState reconstructs current GameState: latest snapshot + events after it.
func (r *Router) loadState(ctx context.Context, gameID uuid.UUID) (*pb.GameState, error) {
	state, snapSeq, err := r.store.GetLatestSnapshot(ctx, gameID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrGameNotFound
		}
		return nil, fmt.Errorf("load snapshot for %s: %w", gameID, err)
	}

	for {
		events, err := r.store.GetEventsForGame(ctx, gameID, snapSeq+1, 500)
		if err != nil {
			return nil, fmt.Errorf("load events for %s after seq %d: %w", gameID, snapSeq, err)
		}
		if len(events) == 0 {
			break
		}
		state = applyAll(state, events)
		snapSeq = events[len(events)-1].GetSequence()
	}
	return state, nil
}

// Close shuts down the router and all sessions it manages.
func (r *Router) Close() {
	r.cancel()        // stops the reaper and cancels all session contexts
	r.reaperWG.Wait() // wait for reaper to exit

	r.mu.Lock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.sessions = make(map[uuid.UUID]*Session)
	r.mu.Unlock()

	for _, s := range sessions {
		s.Close() // idempotent; context already cancelled above
	}
	for _, s := range sessions {
		s.Wait()
	}
}

func (r *Router) reapLoop() {
	defer r.reaperWG.Done()
	tick := time.NewTicker(r.opts.ReapInterval)
	defer tick.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-tick.C:
			r.reapOnce()
		}
	}
}

// reapOnce removes sessions whose run goroutines have already exited.
func (r *Router) reapOnce() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, sess := range r.sessions {
		select {
		case <-sess.closed:
			delete(r.sessions, id)
		default:
		}
	}
}

// ActiveSessionCount returns the number of sessions currently in memory.
func (r *Router) ActiveSessionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}