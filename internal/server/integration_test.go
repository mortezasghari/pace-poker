package server_test

import (
	"context"
	"strings"
	"testing"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/server"
	"github.com/pacepoker/poker/internal/store"
	"github.com/pacepoker/poker/internal/testutil"
)

func newIntegrationServer(t *testing.T) *server.Server {
	t.Helper()
	pool := testutil.NewPostgresPool(t, "../../db/migrations")
	return server.New(store.New(pool))
}

func validCfg() *pb.CashGameConfig {
	return &pb.CashGameConfig{
		TableName:         "Test Table",
		Variant:           pb.GameVariant_GAME_VARIANT_TEXAS_HOLDEM,
		Structure:         pb.BettingStructure_BETTING_STRUCTURE_NO_LIMIT,
		Currency:          "USD",
		SmallBlind:        50,
		BigBlind:          100,
		MinBuyIn:          5000,
		MaxBuyIn:          50000,
		MaxSeats:          9,
		MinPlayersToStart: 2,
	}
}

func makeTable(t *testing.T, srv *server.Server, name string, bb int64) string {
	t.Helper()
	cfg := validCfg()
	cfg.TableName = name
	cfg.BigBlind = bb
	cfg.SmallBlind = bb / 2
	resp, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err != nil {
		t.Fatalf("CreateTable(%q, bb=%d): %v", name, bb, err)
	}
	return resp.State.GameId
}

// ── CreateTable ───────────────────────────────────────────────────────────────

func TestCreateTable_Happy(t *testing.T) {
	srv := newIntegrationServer(t)
	resp, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: validCfg()})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if resp.State == nil {
		t.Fatal("expected non-nil state")
	}
	if resp.State.GameId == "" {
		t.Error("expected non-empty game_id")
	}
	if resp.State.Status != pb.GameStatus_GAME_STATUS_WAITING {
		t.Errorf("status: got %v, want WAITING", resp.State.Status)
	}
	if resp.State.Version != 1 {
		t.Errorf("version: got %d, want 1", resp.State.Version)
	}
}

func TestCreateTable_NilConfig(t *testing.T) {
	srv := newIntegrationServer(t)
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{})
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestCreateTable_EmptyTableName(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.TableName = ""
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err == nil {
		t.Fatal("expected error for empty table_name")
	}
}

func TestCreateTable_TableNameTooLong(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.TableName = strings.Repeat("x", 101)
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err == nil {
		t.Fatal("expected error for table_name > 100 chars")
	}
}

func TestCreateTable_InvalidCurrency(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.Currency = "XYZ"
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err == nil {
		t.Fatal("expected error for invalid currency")
	}
}

func TestCreateTable_BigBlindNotGreaterThanSmallBlind(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.BigBlind = cfg.SmallBlind
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err == nil {
		t.Fatal("expected error when big_blind == small_blind")
	}
}

func TestCreateTable_MaxSeatsTooSmall(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.MaxSeats = 1
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err == nil {
		t.Fatal("expected error for max_seats < 2")
	}
}

func TestCreateTable_PrivateTableWithoutPassword(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.IsPrivate = true
	cfg.Password = ""
	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err == nil {
		t.Fatal("expected error for private table without password")
	}
}

func TestCreateTable_Idempotency(t *testing.T) {
	srv := newIntegrationServer(t)
	ctx := context.Background()
	cmdID := "550e8400-e29b-41d4-a716-446655440000"

	resp1, err := srv.CreateTable(ctx, &pb.CreateTableCommand{Config: validCfg(), CommandId: cmdID})
	if err != nil {
		t.Fatalf("first CreateTable: %v", err)
	}
	resp2, err := srv.CreateTable(ctx, &pb.CreateTableCommand{Config: validCfg(), CommandId: cmdID})
	if err != nil {
		t.Fatalf("idempotent CreateTable: %v", err)
	}
	if resp1.State.GameId != resp2.State.GameId {
		t.Errorf("idempotency broken: %q vs %q", resp1.State.GameId, resp2.State.GameId)
	}
}

func TestCreateTable_EmptyCommandIDGeneratesDistinctGames(t *testing.T) {
	srv := newIntegrationServer(t)
	ctx := context.Background()

	resp1, err := srv.CreateTable(ctx, &pb.CreateTableCommand{Config: validCfg()})
	if err != nil {
		t.Fatalf("first CreateTable: %v", err)
	}
	resp2, err := srv.CreateTable(ctx, &pb.CreateTableCommand{Config: validCfg()})
	if err != nil {
		t.Fatalf("second CreateTable: %v", err)
	}
	if resp1.State.GameId == resp2.State.GameId {
		t.Error("empty command_id should not trigger idempotency — got same game_id twice")
	}
}

// ── SearchTables ──────────────────────────────────────────────────────────────

func TestSearchTables_Empty(t *testing.T) {
	srv := newIntegrationServer(t)
	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{})
	if err != nil {
		t.Fatalf("SearchTables: %v", err)
	}
	if len(resp.Tables) != 0 {
		t.Errorf("expected 0 tables, got %d", len(resp.Tables))
	}
	if resp.TotalCount != 0 {
		t.Errorf("expected total_count=0, got %d", resp.TotalCount)
	}
}

func TestSearchTables_ListsAll(t *testing.T) {
	srv := newIntegrationServer(t)
	makeTable(t, srv, "Alpha", 100)
	makeTable(t, srv, "Beta", 200)
	makeTable(t, srv, "Gamma", 300)

	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{})
	if err != nil {
		t.Fatalf("SearchTables: %v", err)
	}
	if len(resp.Tables) != 3 {
		t.Errorf("expected 3 tables, got %d", len(resp.Tables))
	}
	if resp.TotalCount != 3 {
		t.Errorf("expected total_count=3, got %d", resp.TotalCount)
	}
}

func TestSearchTables_NameFilter(t *testing.T) {
	srv := newIntegrationServer(t)
	makeTable(t, srv, "High Roller", 1000)
	makeTable(t, srv, "Low Stakes", 100)
	makeTable(t, srv, "High Noon", 500)

	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{NameQuery: "high"})
	if err != nil {
		t.Fatalf("SearchTables: %v", err)
	}
	if len(resp.Tables) != 2 {
		t.Errorf("expected 2 tables matching 'high', got %d", len(resp.Tables))
	}
}

func TestSearchTables_BigBlindFilter(t *testing.T) {
	srv := newIntegrationServer(t)
	makeTable(t, srv, "NL100a", 100)
	makeTable(t, srv, "NL200", 200)
	makeTable(t, srv, "NL100b", 100)

	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{BigBlind: 100})
	if err != nil {
		t.Fatalf("SearchTables: %v", err)
	}
	if len(resp.Tables) != 2 {
		t.Errorf("expected 2 tables with bb=100, got %d", len(resp.Tables))
	}
}

func TestSearchTables_Pagination(t *testing.T) {
	srv := newIntegrationServer(t)
	for i := 0; i < 5; i++ {
		makeTable(t, srv, "Table", 100)
	}

	page1, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Tables) != 2 {
		t.Errorf("page1: expected 2, got %d", len(page1.Tables))
	}
	if page1.TotalCount != 5 {
		t.Errorf("page1 total_count: expected 5, got %d", page1.TotalCount)
	}

	page2, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Tables) != 2 {
		t.Errorf("page2: expected 2, got %d", len(page2.Tables))
	}

	page3, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3.Tables) != 1 {
		t.Errorf("page3: expected 1, got %d", len(page3.Tables))
	}
}

func TestSearchTables_NegativeOffsetRejected(t *testing.T) {
	srv := newIntegrationServer(t)
	_, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{Offset: -1})
	if err == nil {
		t.Fatal("expected error for negative offset")
	}
}

func TestSearchTables_LimitZeroDefaultsTo50(t *testing.T) {
	srv := newIntegrationServer(t)
	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{Limit: 0})
	if err != nil {
		t.Fatalf("SearchTables limit=0: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestSearchTables_LimitClampedTo200(t *testing.T) {
	srv := newIntegrationServer(t)
	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{Limit: 9999})
	if err != nil {
		t.Fatalf("SearchTables limit=9999: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestSearchTables_TableSummaryFields(t *testing.T) {
	srv := newIntegrationServer(t)
	cfg := validCfg()
	cfg.TableName = "Field Check"
	cfg.BigBlind = 200
	cfg.SmallBlind = 100

	_, err := srv.CreateTable(context.Background(), &pb.CreateTableCommand{Config: cfg})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	resp, err := srv.SearchTables(context.Background(), &pb.SearchTablesRequest{NameQuery: "Field Check"})
	if err != nil {
		t.Fatalf("SearchTables: %v", err)
	}
	if len(resp.Tables) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Tables))
	}

	tbl := resp.Tables[0]
	if tbl.TableName != "Field Check" {
		t.Errorf("table_name: got %q", tbl.TableName)
	}
	if tbl.BigBlind != 200 {
		t.Errorf("big_blind: got %d, want 200", tbl.BigBlind)
	}
	if tbl.Currency != "USD" {
		t.Errorf("currency: got %q, want USD", tbl.Currency)
	}
	if tbl.GameId == "" {
		t.Error("expected non-empty game_id")
	}
	if tbl.CreatedAt == nil {
		t.Error("expected non-nil created_at")
	}
}