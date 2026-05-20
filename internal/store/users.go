package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pacepoker/poker/internal/store/pgstore"
)

// StepsPerChip is the exchange rate: 1 step earns this many chips.
// Hardcoded 1:1 for now; change in one place to update the whole system.
const StepsPerChip int64 = 1

// ── Domain types ──────────────────────────────────────────────────────────────

// Date represents a calendar date with no timezone attached.
// Equivalent to civil.Date from cloud.google.com/go/civil.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the Date of the given time in that time's location.
func DateOf(t time.Time) Date {
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

// User is the store-layer representation of a user row.
type User struct {
	ID              uuid.UUID
	DisplayName     string
	ExternalID      string // empty if unset
	Timezone        string
	MaxDailyDeposit int64
	CreatedAt       time.Time
}

// UserSnapshot is the denormalized balance projection for a user.
type UserSnapshot struct {
	UserID         uuid.UUID
	ChipBalance    int64
	TotalDeposited int64
	TotalReports   int64
	LastDepositAt  *time.Time // nil if never deposited
	LastActivityAt time.Time
}

// UserInput holds fields for creating a new user.
type UserInput struct {
	ID              uuid.UUID // uuid.Nil = generate a new one
	DisplayName     string
	ExternalID      string // empty = no external_id
	Timezone        string // empty = "UTC"
	MaxDailyDeposit int64
}

// UpdateUserSettingsInput holds mutable fields for a user update.
// Empty string fields mean "no change". MaxDailyDeposit < 0 means no change.
type UpdateUserSettingsInput struct {
	UserID          uuid.UUID
	DisplayName     string
	Timezone        string
	MaxDailyDeposit int64 // negative = no change; 0 = uncapped; positive = cap
}

// ReportStepsInput is the input to the deposit transaction.
type ReportStepsInput struct {
	UserID               uuid.UUID
	ReportID             uuid.UUID
	CumulativeStepsToday int64
	ClientReportedAt     time.Time // informational only
	LocalDate            Date      // computed by service from user's timezone
}

// ReportStepsResult is returned by a successful ReportSteps call.
type ReportStepsResult struct {
	CreditedChips  int64
	NewBalance     int64
	DailyHighWater int64
	Reason         string
	ProcessedAt    time.Time
}

// DepositReport is one row from step_deposit_reports.
type DepositReport struct {
	ReportID      uuid.UUID
	UserID        uuid.UUID
	ReportedSteps int64
	CreditedChips int64
	LocalDate     Date
	ReportedAt    time.Time
	Reason        string
}

// ── pgtype helpers ────────────────────────────────────────────────────────────

func dateToPgtype(d Date) pgtype.Date {
	return pgtype.Date{
		Time:  time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC),
		Valid: true,
	}
}

func pgtypeToDate(d pgtype.Date) Date {
	return Date{Year: d.Time.Year(), Month: d.Time.Month(), Day: d.Time.Day()}
}

func pgtypeToTimePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

// ── Row converters ────────────────────────────────────────────────────────────

func rowToUser(r pgstore.User) User {
	ext := ""
	if r.ExternalID != nil {
		ext = *r.ExternalID
	}
	return User{
		ID:              r.ID,
		DisplayName:     r.DisplayName,
		ExternalID:      ext,
		Timezone:        r.Timezone,
		MaxDailyDeposit: r.MaxDailyDeposit,
		CreatedAt:       r.CreatedAt.Time,
	}
}

func rowToUserSnapshot(r pgstore.UserSnapshot) UserSnapshot {
	return UserSnapshot{
		UserID:         r.UserID,
		ChipBalance:    r.ChipBalance,
		TotalDeposited: r.TotalDeposited,
		TotalReports:   r.TotalReports,
		LastDepositAt:  pgtypeToTimePtr(r.LastDepositAt),
		LastActivityAt: r.LastActivityAt.Time,
	}
}

func rowToDepositReport(r pgstore.StepDepositReport) DepositReport {
	return DepositReport{
		ReportID:      r.ReportID,
		UserID:        r.UserID,
		ReportedSteps: r.ReportedSteps,
		CreditedChips: r.CreditedChips,
		LocalDate:     pgtypeToDate(r.LocalDate),
		ReportedAt:    r.ReportedAt.Time,
		Reason:        r.Reason,
	}
}

// ── Store implementations ─────────────────────────────────────────────────────

func (s *pgStore) CreateUser(ctx context.Context, in UserInput) (User, UserSnapshot, error) {
	id := in.ID
	if id == uuid.Nil {
		var err error
		id, err = uuid.NewV7()
		if err != nil {
			return User{}, UserSnapshot{}, fmt.Errorf("generate user id: %w", err)
		}
	}
	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}

	var (
		user User
		snap UserSnapshot
	)
	err := s.WithTx(ctx, func(tx Store) error {
		ps := tx.(*pgStore)

		row, err := ps.queries.CreateUser(ctx, pgstore.CreateUserParams{
			ID:              id,
			DisplayName:     in.DisplayName,
			Column3:         in.ExternalID, // NULLIF($3,'') in SQL
			Column4:         tz,            // COALESCE(NULLIF($4,''),'UTC')
			MaxDailyDeposit: in.MaxDailyDeposit,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicateExternalID
			}
			return fmt.Errorf("insert user: %w", err)
		}
		user = rowToUser(row)

		snapRow, err := ps.queries.CreateUserSnapshot(ctx, id)
		if err != nil {
			return fmt.Errorf("create user snapshot: %w", err)
		}
		snap = rowToUserSnapshot(snapRow)
		return nil
	})
	return user, snap, err
}

func (s *pgStore) GetUser(ctx context.Context, userID uuid.UUID) (User, UserSnapshot, error) {
	row, err := s.queries.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, UserSnapshot{}, ErrNotFound
		}
		return User{}, UserSnapshot{}, fmt.Errorf("get user: %w", err)
	}
	snapRow, err := s.queries.GetUserSnapshot(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, UserSnapshot{}, ErrNotFound
		}
		return User{}, UserSnapshot{}, fmt.Errorf("get user snapshot: %w", err)
	}
	return rowToUser(row), rowToUserSnapshot(snapRow), nil
}

func (s *pgStore) GetUserByExternalID(ctx context.Context, externalID string) (User, UserSnapshot, error) {
	row, err := s.queries.GetUserByExternalID(ctx, &externalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, UserSnapshot{}, ErrNotFound
		}
		return User{}, UserSnapshot{}, fmt.Errorf("get user by external_id: %w", err)
	}
	snapRow, err := s.queries.GetUserSnapshot(ctx, row.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, UserSnapshot{}, ErrNotFound
		}
		return User{}, UserSnapshot{}, fmt.Errorf("get user snapshot: %w", err)
	}
	return rowToUser(row), rowToUserSnapshot(snapRow), nil
}

func (s *pgStore) UpdateUserSettings(ctx context.Context, in UpdateUserSettingsInput) (User, error) {
	row, err := s.queries.UpdateUserSettings(ctx, pgstore.UpdateUserSettingsParams{
		ID:      in.UserID,
		Column2: in.DisplayName,        // COALESCE(NULLIF($2,''), display_name)
		Column3: in.Timezone,           // COALESCE(NULLIF($3,''), timezone)
		Column4: in.MaxDailyDeposit,    // CASE WHEN $4<0 THEN existing ELSE $4 END
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("update user settings: %w", err)
	}
	return rowToUser(row), nil
}

func (s *pgStore) GetUserSnapshot(ctx context.Context, userID uuid.UUID) (UserSnapshot, error) {
	row, err := s.queries.GetUserSnapshot(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserSnapshot{}, ErrNotFound
		}
		return UserSnapshot{}, fmt.Errorf("get user snapshot: %w", err)
	}
	return rowToUserSnapshot(row), nil
}

func (s *pgStore) ReportSteps(ctx context.Context, in ReportStepsInput) (ReportStepsResult, error) {
	var result ReportStepsResult
	err := s.WithTx(ctx, func(tx Store) error {
		ps := tx.(*pgStore)
		localDatePg := dateToPgtype(in.LocalDate)

		// 1. Idempotency: was this report_id already processed?
		existing, err := ps.queries.FindDepositReportByReportID(ctx, pgstore.FindDepositReportByReportIDParams{
			UserID:   in.UserID,
			ReportID: in.ReportID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("idempotency check: %w", err)
		}
		if err == nil {
			// Already processed — reconstruct result from the stored audit row.
			snapRow, sErr := ps.queries.GetUserSnapshot(ctx, in.UserID)
			if sErr != nil {
				return fmt.Errorf("snapshot for idempotency replay: %w", sErr)
			}
			hwm := existing.ReportedSteps
			daily, dErr := ps.queries.GetDailyStepsForUpdate(ctx, pgstore.GetDailyStepsForUpdateParams{
				UserID: in.UserID, LocalDate: localDatePg,
			})
			if dErr == nil {
				hwm = daily.MaxSteps
			}
			result = ReportStepsResult{
				CreditedChips:  existing.CreditedChips,
				NewBalance:     snapRow.ChipBalance,
				DailyHighWater: hwm,
				Reason:         existing.Reason,
				ProcessedAt:    existing.ReportedAt.Time,
			}
			return nil
		}

		// 2. Lock the high-water mark row for this user/day (SELECT FOR UPDATE).
		var prevHWM int64
		daily, err := ps.queries.GetDailyStepsForUpdate(ctx, pgstore.GetDailyStepsForUpdateParams{
			UserID: in.UserID, LocalDate: localDatePg,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lock daily steps: %w", err)
		}
		if err == nil {
			prevHWM = daily.MaxSteps
		}

		// 3. Compute delta and classify reason.
		var delta int64
		var reason string
		switch {
		case in.CumulativeStepsToday < prevHWM:
			delta, reason = 0, "NO_OP_LOWER"
		case in.CumulativeStepsToday == prevHWM && prevHWM > 0:
			delta, reason = 0, "NO_OP_DUPLICATE_VALUE"
		case prevHWM == 0:
			delta, reason = in.CumulativeStepsToday, "NEW_DAY"
		default:
			delta, reason = in.CumulativeStepsToday-prevHWM, "INCREMENT"
		}

		// 4. Apply daily cap if configured.
		if delta > 0 {
			userRow, uErr := ps.queries.GetUser(ctx, in.UserID)
			if uErr != nil {
				return fmt.Errorf("get user for cap check: %w", uErr)
			}
			if userRow.MaxDailyDeposit > 0 {
				credited, sErr := ps.queries.SumCreditedToday(ctx, pgstore.SumCreditedTodayParams{
					UserID: in.UserID, LocalDate: localDatePg,
				})
				if sErr != nil {
					return fmt.Errorf("sum credited today: %w", sErr)
				}
				remaining := userRow.MaxDailyDeposit - credited
				switch {
				case remaining <= 0:
					delta, reason = 0, "CAP_EXCEEDED"
				case delta > remaining:
					delta, reason = remaining, "CAP_PARTIAL"
				}
			}
		}

		// 5. Upsert high-water mark (GREATEST ensures no-op if new value is lower).
		updatedDaily, err := ps.queries.UpsertDailyStepsHighWaterMark(ctx,
			pgstore.UpsertDailyStepsHighWaterMarkParams{
				UserID:    in.UserID,
				LocalDate: localDatePg,
				MaxSteps:  in.CumulativeStepsToday,
			})
		if err != nil {
			return fmt.Errorf("upsert daily steps: %w", err)
		}

		// 6. Insert audit row. ON CONFLICT DO NOTHING handles the narrow race
		//    where two identical report_ids both pass step 1 concurrently.
		report, err := ps.queries.InsertDepositReport(ctx, pgstore.InsertDepositReportParams{
			UserID:        in.UserID,
			ReportID:      in.ReportID,
			ReportedSteps: in.CumulativeStepsToday,
			CreditedChips: delta * StepsPerChip,
			LocalDate:     localDatePg,
			Reason:        reason,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Conflict fired — fetch the row the winner inserted.
			report, err = ps.queries.FindDepositReportByReportID(ctx,
				pgstore.FindDepositReportByReportIDParams{
					UserID: in.UserID, ReportID: in.ReportID,
				})
		}
		if err != nil {
			return fmt.Errorf("insert deposit report: %w", err)
		}

		// 7. Apply to snapshot (always bumps total_reports + last_activity_at).
		snapRow, err := ps.queries.ApplyDepositToSnapshot(ctx, pgstore.ApplyDepositToSnapshotParams{
			UserID:      in.UserID,
			ChipBalance: delta * StepsPerChip,
		})
		if err != nil {
			return fmt.Errorf("apply deposit to snapshot: %w", err)
		}

		result = ReportStepsResult{
			CreditedChips:  delta * StepsPerChip,
			NewBalance:     snapRow.ChipBalance,
			DailyHighWater: updatedDaily.MaxSteps,
			Reason:         reason,
			ProcessedAt:    report.ReportedAt.Time,
		}
		return nil
	})
	return result, err
}

func (s *pgStore) ListDepositReports(ctx context.Context, userID uuid.UUID, limit, offset int32) ([]DepositReport, int64, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}

	rows, err := s.queries.ListDepositReports(ctx, pgstore.ListDepositReportsParams{
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list deposit reports: %w", err)
	}

	total, err := s.queries.CountDepositReports(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("count deposit reports: %w", err)
	}

	out := make([]DepositReport, len(rows))
	for i, r := range rows {
		out[i] = rowToDepositReport(r)
	}
	return out, total, nil
}

func (s *pgStore) FindDepositReport(ctx context.Context, userID, reportID uuid.UUID) (*DepositReport, error) {
	row, err := s.queries.FindDepositReportByReportID(ctx, pgstore.FindDepositReportByReportIDParams{
		UserID: userID, ReportID: reportID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find deposit report: %w", err)
	}
	r := rowToDepositReport(row)
	return &r, nil
}

func (s *pgStore) DebitUserBalance(ctx context.Context, userID uuid.UUID, amount int64) (UserSnapshot, error) {
	row, err := s.queries.DebitUserBalance(ctx, pgstore.DebitUserBalanceParams{
		UserID:      userID,
		ChipBalance: amount,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserSnapshot{}, ErrInsufficientBalance
		}
		return UserSnapshot{}, fmt.Errorf("debit user balance: %w", err)
	}
	return rowToUserSnapshot(row), nil
}

func (s *pgStore) CreditUserBalance(ctx context.Context, userID uuid.UUID, amount int64) (UserSnapshot, error) {
	row, err := s.queries.CreditUserBalance(ctx, pgstore.CreditUserBalanceParams{
		UserID:      userID,
		ChipBalance: amount,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserSnapshot{}, ErrNotFound
		}
		return UserSnapshot{}, fmt.Errorf("credit user balance: %w", err)
	}
	return rowToUserSnapshot(row), nil
}
