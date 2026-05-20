package user

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Clock allows injecting a fake clock in tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Service implements UserServiceServer using the store.
type Service struct {
	pb.UnimplementedUserServiceServer
	store store.Store
	clock Clock
}

// New returns a Service backed by the given store.
func New(s store.Store) *Service {
	return &Service{store: s, clock: realClock{}}
}

// NewWithClock is used by tests to inject a fake clock.
func NewWithClock(s store.Store, c Clock) *Service {
	return &Service{store: s, clock: c}
}

// ── RPC handlers ──────────────────────────────────────────────────────────────

func (svc *Service) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponse, error) {
	var id uuid.UUID
	if req.GetUserId() != "" {
		var err error
		id, err = uuid.Parse(req.GetUserId())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid user_id: %v", err)
		}
	}
	if req.GetDisplayName() == "" {
		return nil, status.Error(codes.InvalidArgument, "display_name is required")
	}

	user, snap, err := svc.store.CreateUser(ctx, store.UserInput{
		ID:              id,
		DisplayName:     req.GetDisplayName(),
		ExternalID:      req.GetExternalId(),
		Timezone:        req.GetTimezone(),
		MaxDailyDeposit: req.GetMaxDailyDeposit(),
	})
	if err != nil {
		if err == store.ErrDuplicateExternalID {
			return nil, status.Error(codes.AlreadyExists, "external_id already in use")
		}
		return nil, status.Errorf(codes.Internal, "create user: %v", err)
	}

	return &pb.CreateUserResponse{
		User:     userToProto(user),
		Snapshot: snapshotToProto(snap),
	}, nil
}

func (svc *Service) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.GetUserResponse, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}

	user, snap, err := svc.store.GetUser(ctx, userID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, status.Errorf(codes.Internal, "get user: %v", err)
	}

	return &pb.GetUserResponse{
		User:     userToProto(user),
		Snapshot: snapshotToProto(snap),
	}, nil
}

func (svc *Service) UpdateUserSettings(ctx context.Context, req *pb.UpdateUserSettingsRequest) (*pb.UpdateUserSettingsResponse, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}

	user, err := svc.store.UpdateUserSettings(ctx, store.UpdateUserSettingsInput{
		UserID:          userID,
		DisplayName:     req.GetDisplayName(),
		Timezone:        req.GetTimezone(),
		MaxDailyDeposit: req.GetMaxDailyDeposit(),
	})
	if err != nil {
		if err == store.ErrNotFound {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, status.Errorf(codes.Internal, "update user settings: %v", err)
	}

	return &pb.UpdateUserSettingsResponse{User: userToProto(user)}, nil
}

func (svc *Service) ReportSteps(ctx context.Context, req *pb.ReportStepsRequest) (*pb.ReportStepsResponse, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	reportID, err := uuid.Parse(req.GetReportId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid report_id: %v", err)
	}
	if req.GetCumulativeStepsToday() < 0 {
		return nil, status.Error(codes.InvalidArgument, "cumulative_steps_today must be non-negative")
	}

	// Determine the user's local date from their stored timezone.
	localDate, err := svc.localDateForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	var clientAt time.Time
	if req.GetClientReportedAt() != nil {
		clientAt = req.GetClientReportedAt().AsTime()
	} else {
		clientAt = svc.clock.Now()
	}

	result, err := svc.store.ReportSteps(ctx, store.ReportStepsInput{
		UserID:               userID,
		ReportID:             reportID,
		CumulativeStepsToday: req.GetCumulativeStepsToday(),
		ClientReportedAt:     clientAt,
		LocalDate:            localDate,
	})
	if err != nil {
		if err == store.ErrNotFound {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, status.Errorf(codes.Internal, "report steps: %v", err)
	}

	return &pb.ReportStepsResponse{
		CreditedChips:  result.CreditedChips,
		NewBalance:     result.NewBalance,
		DailyHighWater: result.DailyHighWater,
		Reason:         result.Reason,
		ProcessedAt:    timestamppb.New(result.ProcessedAt),
	}, nil
}

func (svc *Service) GetUserSnapshot(ctx context.Context, req *pb.GetUserSnapshotRequest) (*pb.GetUserSnapshotResponse, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}

	snap, err := svc.store.GetUserSnapshot(ctx, userID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, status.Error(codes.NotFound, "user not found")
		}
		return nil, status.Errorf(codes.Internal, "get user snapshot: %v", err)
	}

	return &pb.GetUserSnapshotResponse{Snapshot: snapshotToProto(snap)}, nil
}

func (svc *Service) ListDepositReports(ctx context.Context, req *pb.ListDepositReportsRequest) (*pb.ListDepositReportsResponse, error) {
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}

	limit := req.GetLimit()
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}

	reports, total, err := svc.store.ListDepositReports(ctx, userID, limit, req.GetOffset())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list deposit reports: %v", err)
	}

	out := make([]*pb.DepositReport, len(reports))
	for i, r := range reports {
		out[i] = depositReportToProto(r)
	}

	return &pb.ListDepositReportsResponse{
		Reports:    out,
		TotalCount: total,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func parseUserID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "invalid user_id: %v", err)
	}
	return id, nil
}

// localDateForUser fetches the user's timezone from the store and returns
// today's date in that timezone.
func (svc *Service) localDateForUser(ctx context.Context, userID uuid.UUID) (store.Date, error) {
	user, _, err := svc.store.GetUser(ctx, userID)
	if err != nil {
		if err == store.ErrNotFound {
			return store.Date{}, status.Error(codes.NotFound, "user not found")
		}
		return store.Date{}, status.Errorf(codes.Internal, "get user timezone: %v", err)
	}
	tz := user.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Stored timezone is corrupt; fall back to UTC rather than failing.
		loc = time.UTC
	}
	return store.DateOf(svc.clock.Now().In(loc)), nil
}

func userToProto(u store.User) *pb.User {
	return &pb.User{
		Id:              u.ID.String(),
		DisplayName:     u.DisplayName,
		ExternalId:      u.ExternalID,
		Timezone:        u.Timezone,
		MaxDailyDeposit: u.MaxDailyDeposit,
		CreatedAt:       timestamppb.New(u.CreatedAt),
	}
}

func snapshotToProto(s store.UserSnapshot) *pb.UserSnapshot {
	snap := &pb.UserSnapshot{
		UserId:         s.UserID.String(),
		ChipBalance:    s.ChipBalance,
		TotalDeposited: s.TotalDeposited,
		TotalReports:   s.TotalReports,
		LastActivityAt: timestamppb.New(s.LastActivityAt),
	}
	if s.LastDepositAt != nil {
		snap.LastDepositAt = timestamppb.New(*s.LastDepositAt)
	}
	return snap
}

func depositReportToProto(r store.DepositReport) *pb.DepositReport {
	return &pb.DepositReport{
		ReportId:      r.ReportID.String(),
		ReportedSteps: r.ReportedSteps,
		CreditedChips: r.CreditedChips,
		LocalDate:     fmt.Sprintf("%04d-%02d-%02d", r.LocalDate.Year, int(r.LocalDate.Month), r.LocalDate.Day),
		ReportedAt:    timestamppb.New(r.ReportedAt),
		Reason:        r.Reason,
	}
}
