package store_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pacepoker/poker/internal/store"
	"github.com/pacepoker/poker/internal/testutil"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	pool := testutil.NewPostgresPool(t, "../../db/migrations")
	return store.New(pool)
}

func makeUser(t *testing.T, st store.Store, extra ...func(*store.UserInput)) (store.User, store.UserSnapshot) {
	t.Helper()
	in := store.UserInput{
		DisplayName: "Alice",
		Timezone:    "UTC",
	}
	for _, fn := range extra {
		fn(&in)
	}
	u, s, err := st.CreateUser(t.Context(), in)
	if err != nil {
		t.Fatalf("makeUser: %v", err)
	}
	return u, s
}

// ── CreateUser ────────────────────────────────────────────────────────────────

func TestCreateUser_GeneratesID(t *testing.T) {
	st := newStore(t)
	u, snap, err := st.CreateUser(t.Context(), store.UserInput{DisplayName: "Bob"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID == uuid.Nil {
		t.Error("expected a non-nil UUID to be generated")
	}
	if snap.UserID != u.ID {
		t.Errorf("snapshot user_id mismatch: got %s want %s", snap.UserID, u.ID)
	}
	if snap.ChipBalance != 0 {
		t.Errorf("new user should have zero balance, got %d", snap.ChipBalance)
	}
}

func TestCreateUser_SuppliedIDPreserved(t *testing.T) {
	st := newStore(t)
	id := uuid.New()
	u, _, err := st.CreateUser(t.Context(), store.UserInput{ID: id, DisplayName: "Carol"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID != id {
		t.Errorf("ID not preserved: got %s want %s", u.ID, id)
	}
}

func TestCreateUser_DefaultTimezone(t *testing.T) {
	st := newStore(t)
	u, _, err := st.CreateUser(t.Context(), store.UserInput{DisplayName: "Dave"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Timezone != "UTC" {
		t.Errorf("expected UTC timezone, got %q", u.Timezone)
	}
}

func TestCreateUser_DuplicateExternalID(t *testing.T) {
	st := newStore(t)
	_, _, err := st.CreateUser(t.Context(), store.UserInput{DisplayName: "Eve", ExternalID: "ext-001"})
	if err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, _, err = st.CreateUser(t.Context(), store.UserInput{DisplayName: "Eve2", ExternalID: "ext-001"})
	if err != store.ErrDuplicateExternalID {
		t.Errorf("expected ErrDuplicateExternalID, got %v", err)
	}
}

// ── GetUser ───────────────────────────────────────────────────────────────────

func TestGetUser_HappyPath(t *testing.T) {
	st := newStore(t)
	created, _ := makeUser(t, st)

	got, snap, err := st.GetUser(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: got %s want %s", got.ID, created.ID)
	}
	if got.DisplayName != "Alice" {
		t.Errorf("DisplayName: got %q want %q", got.DisplayName, "Alice")
	}
	if snap.UserID != created.ID {
		t.Errorf("snapshot user_id mismatch")
	}
}

func TestGetUser_NotFound(t *testing.T) {
	st := newStore(t)
	_, _, err := st.GetUser(t.Context(), uuid.New())
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── GetUserByExternalID ───────────────────────────────────────────────────────

func TestGetUserByExternalID_HappyPath(t *testing.T) {
	st := newStore(t)
	makeUser(t, st, func(in *store.UserInput) {
		in.ExternalID = "ext-abc"
	})

	got, _, err := st.GetUserByExternalID(t.Context(), "ext-abc")
	if err != nil {
		t.Fatalf("GetUserByExternalID: %v", err)
	}
	if got.ExternalID != "ext-abc" {
		t.Errorf("ExternalID: got %q want %q", got.ExternalID, "ext-abc")
	}
}

func TestGetUserByExternalID_NotFound(t *testing.T) {
	st := newStore(t)
	_, _, err := st.GetUserByExternalID(t.Context(), "no-such-ext")
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── UpdateUserSettings ────────────────────────────────────────────────────────

func TestUpdateUserSettings_FullUpdate(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	updated, err := st.UpdateUserSettings(t.Context(), store.UpdateUserSettingsInput{
		UserID:          u.ID,
		DisplayName:     "Alice Updated",
		Timezone:        "America/New_York",
		MaxDailyDeposit: 5000,
	})
	if err != nil {
		t.Fatalf("UpdateUserSettings: %v", err)
	}
	if updated.DisplayName != "Alice Updated" {
		t.Errorf("DisplayName: got %q", updated.DisplayName)
	}
	if updated.Timezone != "America/New_York" {
		t.Errorf("Timezone: got %q", updated.Timezone)
	}
	if updated.MaxDailyDeposit != 5000 {
		t.Errorf("MaxDailyDeposit: got %d", updated.MaxDailyDeposit)
	}
}

func TestUpdateUserSettings_EmptyFieldsPreservesOriginal(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st, func(in *store.UserInput) {
		in.DisplayName = "Original"
		in.Timezone = "Europe/Berlin"
	})

	// Empty strings mean "no change"; MaxDailyDeposit < 0 means "no change".
	updated, err := st.UpdateUserSettings(t.Context(), store.UpdateUserSettingsInput{
		UserID:          u.ID,
		MaxDailyDeposit: -1,
	})
	if err != nil {
		t.Fatalf("UpdateUserSettings: %v", err)
	}
	if updated.DisplayName != "Original" {
		t.Errorf("DisplayName should be unchanged, got %q", updated.DisplayName)
	}
	if updated.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone should be unchanged, got %q", updated.Timezone)
	}
}

func TestUpdateUserSettings_NotFound(t *testing.T) {
	st := newStore(t)
	_, err := st.UpdateUserSettings(t.Context(), store.UpdateUserSettingsInput{
		UserID:      uuid.New(),
		DisplayName: "Ghost",
	})
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── ReportSteps ───────────────────────────────────────────────────────────────

func makeReportInput(userID uuid.UUID, steps int64) store.ReportStepsInput {
	return store.ReportStepsInput{
		UserID:               userID,
		ReportID:             uuid.New(),
		CumulativeStepsToday: steps,
		ClientReportedAt:     time.Now(),
		LocalDate:            store.DateOf(time.Now().UTC()),
	}
}

func TestReportSteps_NewDay(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	result, err := st.ReportSteps(t.Context(), makeReportInput(u.ID, 1000))
	if err != nil {
		t.Fatalf("ReportSteps: %v", err)
	}
	if result.CreditedChips != 1000*store.StepsPerChip {
		t.Errorf("credited: got %d want %d", result.CreditedChips, 1000*store.StepsPerChip)
	}
	if result.Reason != "NEW_DAY" {
		t.Errorf("reason: got %q want NEW_DAY", result.Reason)
	}
	if result.NewBalance != result.CreditedChips {
		t.Errorf("balance %d != credited %d", result.NewBalance, result.CreditedChips)
	}
}

func TestReportSteps_Increment(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	_, _ = st.ReportSteps(t.Context(), makeReportInput(u.ID, 1000))

	result, err := st.ReportSteps(t.Context(), makeReportInput(u.ID, 1500))
	if err != nil {
		t.Fatalf("ReportSteps second: %v", err)
	}
	if result.CreditedChips != 500*store.StepsPerChip {
		t.Errorf("credited delta: got %d want %d", result.CreditedChips, 500*store.StepsPerChip)
	}
	if result.Reason != "INCREMENT" {
		t.Errorf("reason: got %q want INCREMENT", result.Reason)
	}
}

func TestReportSteps_LowerValueNoOp(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	_, _ = st.ReportSteps(t.Context(), makeReportInput(u.ID, 1000))

	result, err := st.ReportSteps(t.Context(), makeReportInput(u.ID, 800))
	if err != nil {
		t.Fatalf("ReportSteps lower: %v", err)
	}
	if result.CreditedChips != 0 {
		t.Errorf("expected 0 chips for lower value, got %d", result.CreditedChips)
	}
	if result.Reason != "NO_OP_LOWER" {
		t.Errorf("reason: got %q want NO_OP_LOWER", result.Reason)
	}
}

func TestReportSteps_IdempotentViaSameReportID(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	in := makeReportInput(u.ID, 1000)
	r1, err := st.ReportSteps(t.Context(), in)
	if err != nil {
		t.Fatalf("first report: %v", err)
	}

	r2, err := st.ReportSteps(t.Context(), in) // same report_id
	if err != nil {
		t.Fatalf("second report (idempotent): %v", err)
	}

	if r2.CreditedChips != r1.CreditedChips {
		t.Errorf("idempotent result mismatch: %d != %d", r2.CreditedChips, r1.CreditedChips)
	}
}

func TestReportSteps_DailyCapExceeded(t *testing.T) {
	st := newStore(t)
	// Cap at 500 chips.
	u, _, err := st.CreateUser(t.Context(), store.UserInput{
		DisplayName:     "Capped",
		MaxDailyDeposit: 500,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Report 1000 steps; only 500 should be credited (the cap).
	result, err := st.ReportSteps(t.Context(), makeReportInput(u.ID, 1000))
	if err != nil {
		t.Fatalf("ReportSteps: %v", err)
	}
	if result.CreditedChips != 500*store.StepsPerChip {
		t.Errorf("credited: got %d want %d", result.CreditedChips, 500*store.StepsPerChip)
	}
	// A second report with more steps should be CAP_EXCEEDED (already at cap).
	result2, err := st.ReportSteps(t.Context(), makeReportInput(u.ID, 2000))
	if err != nil {
		t.Fatalf("ReportSteps2: %v", err)
	}
	if result2.CreditedChips != 0 {
		t.Errorf("expected 0 chips after cap, got %d", result2.CreditedChips)
	}
	if result2.Reason != "CAP_EXCEEDED" {
		t.Errorf("reason: got %q want CAP_EXCEEDED", result2.Reason)
	}
}

func TestReportSteps_DailyCapPartial(t *testing.T) {
	st := newStore(t)
	u, _, err := st.CreateUser(t.Context(), store.UserInput{
		DisplayName:     "Partial",
		MaxDailyDeposit: 700,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// First report: 400 chips → within cap.
	_, _ = st.ReportSteps(t.Context(), makeReportInput(u.ID, 400))
	// Second report: 800 steps increment would give 400 more chips but only 300 remain.
	result, err := st.ReportSteps(t.Context(), makeReportInput(u.ID, 800))
	if err != nil {
		t.Fatalf("ReportSteps partial: %v", err)
	}
	if result.CreditedChips != 300*store.StepsPerChip {
		t.Errorf("partial credit: got %d want %d", result.CreditedChips, 300*store.StepsPerChip)
	}
	if result.Reason != "CAP_PARTIAL" {
		t.Errorf("reason: got %q want CAP_PARTIAL", result.Reason)
	}
}

// ── Balance (debit / credit) ──────────────────────────────────────────────────

func TestDebitCreditUserBalance(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	// Seed balance via ReportSteps.
	_, _ = st.ReportSteps(t.Context(), makeReportInput(u.ID, 1000))

	snap, err := st.DebitUserBalance(t.Context(), u.ID, 300)
	if err != nil {
		t.Fatalf("DebitUserBalance: %v", err)
	}
	if snap.ChipBalance != 700 {
		t.Errorf("after debit: got %d want 700", snap.ChipBalance)
	}

	snap, err = st.CreditUserBalance(t.Context(), u.ID, 100)
	if err != nil {
		t.Fatalf("CreditUserBalance: %v", err)
	}
	if snap.ChipBalance != 800 {
		t.Errorf("after credit: got %d want 800", snap.ChipBalance)
	}
}

func TestDebitUserBalance_InsufficientFunds(t *testing.T) {
	st := newStore(t)
	u, _ := makeUser(t, st)

	// Balance is 0 — any debit should fail.
	_, err := st.DebitUserBalance(t.Context(), u.ID, 1)
	if err != store.ErrInsufficientBalance {
		t.Errorf("expected ErrInsufficientBalance, got %v", err)
	}
}
