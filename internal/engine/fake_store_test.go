package engine

import (
	"context"
	"sync"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/store"
)

// fakeStore implements store.Store for engine tests without Postgres.
type fakeStore struct {
	mu              sync.Mutex
	appendErr       error
	appendCallCount int
	byCommandID     map[string]*pb.GameEvent
	latestSeq       uint64 // tracks max sequence across all appended events

	snapshots     map[string]*pb.GameState
	snapSeqs      map[string]uint64
	snapErr       error
	snapCallCount int
	postSnapEvts  map[string][]*pb.GameEvent

	// appendBlockCh, if non-nil, blocks AppendEvents until closed.
	appendBlockCh chan struct{}
	// appendStartedCh, if non-nil, receives a signal when AppendEvents enters.
	appendStartedCh chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byCommandID:  make(map[string]*pb.GameEvent),
		snapshots:    make(map[string]*pb.GameState),
		snapSeqs:     make(map[string]uint64),
		postSnapEvts: make(map[string][]*pb.GameEvent),
	}
}

func (f *fakeStore) setSnapshot(gameID uuid.UUID, state *pb.GameState, seq uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[gameID.String()] = state
	f.snapSeqs[gameID.String()] = seq
	if seq > f.latestSeq {
		f.latestSeq = seq
	}
}

// ── store.Store implementation ────────────────────────────────────────────────

func (f *fakeStore) AppendEvents(ctx context.Context, evts []*pb.GameEvent) error {
	f.mu.Lock()
	err := f.appendErr
	blockCh := f.appendBlockCh
	startedCh := f.appendStartedCh
	f.mu.Unlock()

	// Signal that AppendEvents has been entered (before any block).
	if startedCh != nil {
		select {
		case startedCh <- struct{}{}:
		default:
		}
	}

	if err != nil {
		return err
	}

	if blockCh != nil {
		<-blockCh
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCallCount++
	for _, evt := range evts {
		if id := evt.GetCausedByCommandId(); id != "" {
			f.byCommandID[id] = evt
		}
		if seq := evt.GetSequence(); seq > f.latestSeq {
			f.latestSeq = seq
		}
	}
	return nil
}

func (f *fakeStore) AppendEvent(ctx context.Context, evt *pb.GameEvent) error {
	return f.AppendEvents(ctx, []*pb.GameEvent{evt})
}

func (f *fakeStore) FindEventByCommandID(ctx context.Context, id uuid.UUID) (*pb.GameEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if evt, ok := f.byCommandID[id.String()]; ok {
		return evt, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetLatestSnapshot(ctx context.Context, gameID uuid.UUID) (*pb.GameState, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapCallCount++
	if f.snapErr != nil {
		return nil, 0, f.snapErr
	}
	state, ok := f.snapshots[gameID.String()]
	if !ok {
		return nil, 0, store.ErrNotFound
	}
	return state, f.snapSeqs[gameID.String()], nil
}

func (f *fakeStore) GetEventsForGame(ctx context.Context, gameID uuid.UUID, fromSeq uint64, limit int32) ([]*pb.GameEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.postSnapEvts[gameID.String()]
	var out []*pb.GameEvent
	for _, e := range all {
		if e.GetSequence() >= fromSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

// Unused by engine tests — satisfy the interface with minimal stubs.

func (f *fakeStore) CreateGameConfig(ctx context.Context, cfg *pb.CashGameConfig) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (f *fakeStore) GetGameConfig(ctx context.Context, id uuid.UUID) (*pb.CashGameConfig, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListActiveGameConfigs(ctx context.Context, limit, offset int32) ([]store.GameConfigSummary, error) {
	return nil, nil
}
func (f *fakeStore) SoftDeleteGameConfig(ctx context.Context, id uuid.UUID) error { return nil }
func (f *fakeStore) SearchGameConfigs(ctx context.Context, filter store.SearchFilter) ([]store.GameConfigSummary, int64, error) {
	return nil, 0, nil
}
func (f *fakeStore) CreateTable(ctx context.Context, cfg *pb.CashGameConfig, id uuid.UUID) (*pb.GameState, error) {
	return nil, nil
}
func (f *fakeStore) GetLatestSequence(ctx context.Context, id uuid.UUID) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latestSeq, nil
}
func (f *fakeStore) CreateSnapshot(ctx context.Context, state *pb.GameState, hand int64) error {
	return nil
}
func (f *fakeStore) GetSnapshotAtOrBefore(ctx context.Context, id uuid.UUID, seq uint64) (*pb.GameState, uint64, error) {
	return nil, 0, store.ErrNotFound
}
func (f *fakeStore) WithTx(ctx context.Context, fn func(store.Store) error) error { return fn(f) }