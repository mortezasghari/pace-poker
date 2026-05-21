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
	"github.com/pacepoker/poker/internal/auth"
	"github.com/pacepoker/poker/internal/config"
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

	authCfg, err := config.LoadAuth0FromEnv()
	if err != nil {
		log.Fatalf("load auth0 config: %v", err)
	}
	cache, err := auth.NewUserCache(10_000)
	if err != nil {
		log.Fatalf("init user cache: %v", err)
	}
	authenticator, err := auth.NewAuthenticator(authCfg, st, cache)
	if err != nil {
		log.Fatalf("init authenticator: %v", err)
	}

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(authenticator.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(authenticator.StreamInterceptor()),
	)
	pb.RegisterPokerServiceServer(grpcServer, server.NewServer(st, router, authenticator))
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