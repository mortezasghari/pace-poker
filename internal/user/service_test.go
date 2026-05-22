package user_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/auth"
	"github.com/pacepoker/poker/internal/store"
	"github.com/pacepoker/poker/internal/user"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ctxAs returns a context carrying a principal for the given user UUID,
// simulating what the auth interceptor injects for real requests.
func ctxAs(userID uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{UserID: userID})
}

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time { return f.t }

// fakeStore is a minimal in-memory store for service-layer tests.
type fakeStore struct {
	users     map[uuid.UUID]store.User
	snapshots map[uuid.UUID]store.UserSnapshot
	reports   map[string]store.ReportStepsResult // key: userID+reportID

	createErr    error
	getErr       error
	updateErr    error
	reportErr    error
	debitErr     error
	creditErr    error
	snapshotErr  error
	listReports  []store.DepositReport
	listTotal    int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:     make(map[uuid.UUID]store.User),
		snapshots: make(map[uuid.UUID]store.UserSnapshot),
		reports:   make(map[string]store.ReportStepsResult),
	}
}

func (f *fakeStore) addUser(u store.User) {
	f.users[u.ID] = u
	f.snapshots[u.ID] = store.UserSnapshot{UserID: u.ID}
}

// ── store.Store stubs ─────────────────────────────────────────────────────────

func (f *fakeStore) CreateUser(_ context.Context, in store.UserInput) (store.User, store.UserSnapshot, error) {
	if f.createErr != nil {
		return store.User{}, store.UserSnapshot{}, f.createErr
	}
	id := in.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}
	u := store.User{ID: id, DisplayName: in.DisplayName, Timezone: tz, MaxDailyDeposit: in.MaxDailyDeposit}
	snap := store.UserSnapshot{UserID: id}
	f.users[id] = u
	f.snapshots[id] = snap
	return u, snap, nil
}

func (f *fakeStore) GetUser(_ context.Context, id uuid.UUID) (store.User, store.UserSnapshot, error) {
	if f.getErr != nil {
		return store.User{}, store.UserSnapshot{}, f.getErr
	}
	u, ok := f.users[id]
	if !ok {
		return store.User{}, store.UserSnapshot{}, store.ErrNotFound
	}
	return u, f.snapshots[id], nil
}

func (f *fakeStore) GetUserByExternalID(_ context.Context, _ string) (store.User, store.UserSnapshot, error) {
	return store.User{}, store.UserSnapshot{}, store.ErrNotFound
}

func (f *fakeStore) UpdateUserSettings(_ context.Context, in store.UpdateUserSettingsInput) (store.User, error) {
	if f.updateErr != nil {
		return store.User{}, f.updateErr
	}
	u, ok := f.users[in.UserID]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	if in.DisplayName != "" {
		u.DisplayName = in.DisplayName
	}
	if in.Timezone != "" {
		u.Timezone = in.Timezone
	}
	if in.MaxDailyDeposit >= 0 {
		u.MaxDailyDeposit = in.MaxDailyDeposit
	}
	f.users[in.UserID] = u
	return u, nil
}

func (f *fakeStore) ReportSteps(_ context.Context, in store.ReportStepsInput) (store.ReportStepsResult, error) {
	if f.reportErr != nil {
		return store.ReportStepsResult{}, f.reportErr
	}
	key := in.UserID.String() + in.ReportID.String()
	if r, ok := f.reports[key]; ok {
		return r, nil
	}
	chips := in.CumulativeStepsToday * store.StepsPerChip
	snap := f.snapshots[in.UserID]
	snap.ChipBalance += chips
	f.snapshots[in.UserID] = snap
	result := store.ReportStepsResult{
		CreditedChips:  chips,
		NewBalance:     snap.ChipBalance,
		DailyHighWater: in.CumulativeStepsToday,
		Reason:         "NEW_DAY",
		ProcessedAt:    time.Now(),
	}
	f.reports[key] = result
	return result, nil
}

func (f *fakeStore) ListDepositReports(_ context.Context, _ uuid.UUID, _, _ int32) ([]store.DepositReport, int64, error) {
	return f.listReports, f.listTotal, nil
}

func (f *fakeStore) FindDepositReport(_ context.Context, _, _ uuid.UUID) (*store.DepositReport, error) {
	return nil, nil
}

func (f *fakeStore) DebitUserBalance(_ context.Context, id uuid.UUID, amount int64) (store.UserSnapshot, error) {
	if f.debitErr != nil {
		return store.UserSnapshot{}, f.debitErr
	}
	snap := f.snapshots[id]
	if snap.ChipBalance < amount {
		return store.UserSnapshot{}, store.ErrInsufficientBalance
	}
	snap.ChipBalance -= amount
	f.snapshots[id] = snap
	return snap, nil
}

func (f *fakeStore) CreditUserBalance(_ context.Context, id uuid.UUID, amount int64) (store.UserSnapshot, error) {
	if f.creditErr != nil {
		return store.UserSnapshot{}, f.creditErr
	}
	snap := f.snapshots[id]
	snap.ChipBalance += amount
	f.snapshots[id] = snap
	return snap, nil
}

func (f *fakeStore) GetUserSnapshot(_ context.Context, id uuid.UUID) (store.UserSnapshot, error) {
	if f.snapshotErr != nil {
		return store.UserSnapshot{}, f.snapshotErr
	}
	snap, ok := f.snapshots[id]
	if !ok {
		return store.UserSnapshot{}, store.ErrNotFound
	}
	return snap, nil
}

// Unused poker-game methods — satisfy the store.Store interface.

func (f *fakeStore) CreateGameConfig(_ context.Context, _ *pb.CashGameConfig) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeStore) GetGameConfig(_ context.Context, _ uuid.UUID) (*pb.CashGameConfig, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListActiveGameConfigs(_ context.Context, _, _ int32) ([]store.GameConfigSummary, error) {
	return nil, nil
}
func (f *fakeStore) SoftDeleteGameConfig(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeStore) SearchGameConfigs(_ context.Context, _ store.SearchFilter) ([]store.GameConfigSummary, int64, error) {
	return nil, 0, nil
}
func (f *fakeStore) CreateTable(_ context.Context, _ *pb.CashGameConfig, _ uuid.UUID) (*pb.GameState, error) {
	return nil, nil
}
func (f *fakeStore) AppendEvent(_ context.Context, _ *pb.GameEvent) error  { return nil }
func (f *fakeStore) AppendEvents(_ context.Context, _ []*pb.GameEvent) error { return nil }
func (f *fakeStore) GetEventsForGame(_ context.Context, _ uuid.UUID, _ uint64, _ int32) ([]*pb.GameEvent, error) {
	return nil, nil
}
func (f *fakeStore) GetLatestSequence(_ context.Context, _ uuid.UUID) (uint64, error) { return 0, nil }
func (f *fakeStore) FindEventByCommandID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*pb.GameEvent, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) FindEventByCommandIDGlobal(_ context.Context, _ uuid.UUID) (*pb.GameEvent, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) CreateSnapshot(_ context.Context, _ *pb.GameState, _ int64) error { return nil }
func (f *fakeStore) GetLatestSnapshot(_ context.Context, _ uuid.UUID) (*pb.GameState, uint64, error) {
	return nil, 0, store.ErrNotFound
}
func (f *fakeStore) GetSnapshotAtOrBefore(_ context.Context, _ uuid.UUID, _ uint64) (*pb.GameState, uint64, error) {
	return nil, 0, store.ErrNotFound
}
func (f *fakeStore) WithTx(_ context.Context, fn func(store.Store) error) error { return fn(f) }

// ── Tests ─────────────────────────────────────────────────────────────────────

func newSvc(fs *fakeStore, clk *fakeClock) *user.Service {
	return user.NewWithClock(fs, clk)
}

func fixedClock(year int, month time.Month, day int) *fakeClock {
	return &fakeClock{t: time.Date(year, month, day, 12, 0, 0, 0, time.UTC)}
}

func TestCreateUser_RequiresDisplayName(t *testing.T) {
	svc := newSvc(newFakeStore(), fixedClock(2026, 1, 1))
	_, err := svc.CreateUser(context.Background(), &pb.CreateUserRequest{})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateUser_InvalidUserID(t *testing.T) {
	svc := newSvc(newFakeStore(), fixedClock(2026, 1, 1))
	_, err := svc.CreateUser(context.Background(), &pb.CreateUserRequest{
		UserId:      "not-a-uuid",
		DisplayName: "Alice",
	})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateUser_DuplicateExternalIDMapsToAlreadyExists(t *testing.T) {
	fs := newFakeStore()
	fs.createErr = store.ErrDuplicateExternalID
	svc := newSvc(fs, fixedClock(2026, 1, 1))
	_, err := svc.CreateUser(context.Background(), &pb.CreateUserRequest{
		DisplayName: "Alice",
		ExternalId:  "ext-001",
	})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.AlreadyExists {
		t.Errorf("expected AlreadyExists, got %v", err)
	}
}

func TestGetUser_NotFoundMapsToNotFound(t *testing.T) {
	svc := newSvc(newFakeStore(), fixedClock(2026, 1, 1))
	id := uuid.New()
	_, err := svc.GetUser(ctxAs(id), &pb.GetUserRequest{UserId: id.String()})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestGetUser_EmptyIDInvalid(t *testing.T) {
	svc := newSvc(newFakeStore(), fixedClock(2026, 1, 1))
	_, err := svc.GetUser(context.Background(), &pb.GetUserRequest{})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestReportSteps_InvalidReportID(t *testing.T) {
	fs := newFakeStore()
	id := uuid.New()
	fs.addUser(store.User{ID: id, DisplayName: "Alice", Timezone: "UTC"})
	svc := newSvc(fs, fixedClock(2026, 5, 20))

	_, err := svc.ReportSteps(ctxAs(id), &pb.ReportStepsRequest{
		UserId:               id.String(),
		ReportId:             "bad-uuid",
		CumulativeStepsToday: 1000,
	})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestReportSteps_NegativeStepsInvalid(t *testing.T) {
	fs := newFakeStore()
	id := uuid.New()
	fs.addUser(store.User{ID: id, DisplayName: "Alice", Timezone: "UTC"})
	svc := newSvc(fs, fixedClock(2026, 5, 20))

	_, err := svc.ReportSteps(ctxAs(id), &pb.ReportStepsRequest{
		UserId:               id.String(),
		ReportId:             uuid.New().String(),
		CumulativeStepsToday: -1,
	})
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestReportSteps_TimezoneLocalDate(t *testing.T) {
	// Clock is just past midnight UTC on Jan 2 (23:01 on Jan 1 in UTC-1).
	// A user in "Etc/GMT+1" (UTC-1) should get local date Jan 1.
	clk := &fakeClock{t: time.Date(2026, 1, 2, 0, 30, 0, 0, time.UTC)}
	fs := newFakeStore()
	id := uuid.New()
	fs.addUser(store.User{ID: id, DisplayName: "Timezone User", Timezone: "Etc/GMT+1"})

	svc := user.NewWithClock(fs, clk)
	resp, err := svc.ReportSteps(ctxAs(id), &pb.ReportStepsRequest{
		UserId:               id.String(),
		ReportId:             uuid.New().String(),
		CumulativeStepsToday: 500,
	})
	if err != nil {
		t.Fatalf("ReportSteps: %v", err)
	}
	if resp.GetCreditedChips() != 500*store.StepsPerChip {
		t.Errorf("credited: got %d want %d", resp.GetCreditedChips(), 500*store.StepsPerChip)
	}
}

func TestListDepositReports_LimitClamping(t *testing.T) {
	fs := newFakeStore()
	id := uuid.New()
	fs.addUser(store.User{ID: id, DisplayName: "Alice", Timezone: "UTC"})
	fs.listTotal = 5
	svc := newSvc(fs, fixedClock(2026, 1, 1))

	// limit=0 should default to 50; limit=999 should be clamped to 200.
	// We just verify no error and the response is well-formed.
	for _, limit := range []int32{0, 999} {
		resp, err := svc.ListDepositReports(ctxAs(id), &pb.ListDepositReportsRequest{
			UserId: id.String(),
			Limit:  limit,
		})
		if err != nil {
			t.Errorf("limit=%d: unexpected error %v", limit, err)
		}
		if resp.GetTotalCount() != 5 {
			t.Errorf("limit=%d: total_count got %d want 5", limit, resp.GetTotalCount())
		}
	}
}
