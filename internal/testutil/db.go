package testutil

import (
	"context"
	"database/sql"
	"log"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NewPostgresPool starts a throwaway Postgres container, runs all migrations,
// and returns a pgxpool connected to it. The container is terminated when t
// (or the test binary) exits.
func NewPostgresPool(t *testing.T, migrationsDir string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("poker"),
		tcpostgres.WithUsername("poker"),
		tcpostgres.WithPassword("poker"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get connection string: %v", err)
	}

	sqlDB, err := sql.Open("pgx", connStr)
	if err != nil {
		log.Fatalf("open sql db: %v", err)
	}
	defer sqlDB.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("goose set dialect: %v", err)
	}
	if err := goose.Up(sqlDB, migrationsDir); err != nil {
		log.Fatalf("goose up: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		log.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}