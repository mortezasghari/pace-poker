package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/engine"
	"github.com/pacepoker/poker/internal/server"
	"github.com/pacepoker/poker/internal/store"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", ":9090", "gRPC listen address")
	dsn := flag.String("dsn", envOrDefault("DATABASE_URL", "postgres://poker:poker@localhost:5432/poker"), "Postgres DSN")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	st := store.New(pool)
	router := engine.NewRouter(ctx, st, engine.RouterOptions{})
	defer router.Close()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterPokerServiceServer(grpcServer, server.NewServer(st, router))
	pb.RegisterUserServiceServer(grpcServer, server.NewUserServer(st))

	go func() {
		log.Printf("pacepoker listening on %s", *addr)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	grpcServer.GracefulStop()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}