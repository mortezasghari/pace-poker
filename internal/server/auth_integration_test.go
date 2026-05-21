package server_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/auth0/go-jwt-middleware/v2/jwks"
	"github.com/auth0/go-jwt-middleware/v2/validator"
	"github.com/google/uuid"
	pb "github.com/pacepoker/poker/gen/go/poker/v1"
	"github.com/pacepoker/poker/internal/auth"
	"github.com/pacepoker/poker/internal/config"
	"github.com/pacepoker/poker/internal/server"
	"github.com/pacepoker/poker/internal/store"
	"github.com/lestrrat-go/jwx/v2/jwa"
	jwxjwk "github.com/lestrrat-go/jwx/v2/jwk"
	jwxjwt "github.com/lestrrat-go/jwx/v2/jwt"
	jose "gopkg.in/go-jose/go-jose.v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const (
	integTestAudience = "https://api.poker.integtest"
	integTestNS       = "https://api.poker.integtest/"
	bufSize           = 1 << 20 // 1 MiB
)

// integRig wires a full in-process gRPC server (real interceptors, fake store)
// against a local httptest JWKS server.
type integRig struct {
	rsaKey *rsa.PrivateKey
	issuer string
	conn   *grpc.ClientConn
	client pb.PokerServiceClient
}

func newIntegRig(t *testing.T) *integRig {
	t.Helper()

	// ── RSA key + JWKS server ─────────────────────────────────────────────
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	jwkSet := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       &key.PublicKey,
			KeyID:     "test-key-1",
			Algorithm: "RS256",
			Use:       "sig",
		}},
	}
	jwksJSON, _ := json.Marshal(jwkSet)

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksJSON)
	})
	jwksSrv := httptest.NewServer(mux)
	t.Cleanup(jwksSrv.Close)

	issuer := jwksSrv.URL + "/"
	jwksURL, _ := url.Parse(jwksSrv.URL + "/jwks")
	issuerURL, _ := url.Parse(issuer)

	provider := jwks.NewCachingProvider(issuerURL, 5*time.Minute, jwks.WithCustomJWKSURI(jwksURL))
	v, err := validator.New(
		provider.KeyFunc,
		validator.RS256,
		issuer,
		[]string{integTestAudience},
		validator.WithCustomClaims(func() validator.CustomClaims {
			return &integTestCustomClaims{}
		}),
		validator.WithAllowedClockSkew(30*time.Second),
	)
	if err != nil {
		t.Fatalf("validator.New: %v", err)
	}

	// ── Authenticator + in-memory store ──────────────────────────────────
	st := newIntegFakeStore()
	cfg := config.Auth0Config{
		Domain:          "test.auth0.local",
		Audience:        integTestAudience,
		ClaimsNamespace: integTestNS,
	}
	authenticator := auth.NewAuthenticatorForTest(cfg, v, st, nil)

	// ── In-process gRPC server ────────────────────────────────────────────
	lis := bufconn.Listen(bufSize)
	t.Cleanup(func() { _ = lis.Close() })

	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(authenticator.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(authenticator.StreamInterceptor()),
	)
	fakeRouter := &noopRouter{}
	pb.RegisterPokerServiceServer(grpcSrv, server.NewServer(st, fakeRouter, authenticator))

	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			// Server stopped — normal on test cleanup.
		}
	}()
	t.Cleanup(grpcSrv.Stop)

	// ── Client ───────────────────────────────────────────────────────────
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &integRig{
		rsaKey: key,
		issuer: issuer,
		conn:   conn,
		client: pb.NewPokerServiceClient(conn),
	}
}

// bearerCtx returns a context with the token in gRPC metadata.
func (r *integRig) bearerCtx(t *testing.T, sub string) context.Context {
	t.Helper()
	tok, err := jwxjwt.NewBuilder().
		Subject(sub).
		Issuer(r.issuer).
		Audience([]string{integTestAudience}).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	privJWK, err := jwxjwk.FromRaw(r.rsaKey)
	if err != nil {
		t.Fatalf("jwk.FromRaw: %v", err)
	}
	_ = privJWK.Set(jwxjwk.KeyIDKey, "test-key-1")
	signed, err := jwxjwt.Sign(tok, jwxjwt.WithKey(jwa.RS256, privJWK))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	md := metadata.New(map[string]string{"authorization": "Bearer " + string(signed)})
	return metadata.NewOutgoingContext(context.Background(), md)
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestServer_JoinTable_RequiresAuth(t *testing.T) {
	rig := newIntegRig(t)

	// Call without any token — should be rejected by the interceptor.
	_, err := rig.client.JoinTable(context.Background(), &pb.JoinTableCommand{
		GameId: uuid.New().String(),
		BuyIn:  100,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	s, ok := status.FromError(err)
	if !ok || s.Code() != codes.Unauthenticated {
		t.Errorf("got %v, want Unauthenticated", err)
	}
}

func TestServer_JoinTable_UsesAuthenticatedUserID(t *testing.T) {
	rig := newIntegRig(t)
	ctx := rig.bearerCtx(t, "auth0|user-a")

	// The request will fail at the store level (game not found), but auth
	// must have succeeded — we must NOT get Unauthenticated.
	_, err := rig.client.JoinTable(ctx, &pb.JoinTableCommand{
		GameId: uuid.New().String(),
		BuyIn:  100,
	})
	// Auth is OK if we get any error that isn't Unauthenticated.
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unauthenticated {
			t.Fatalf("request was rejected as Unauthenticated despite valid token: %v", err)
		}
		// Some other error (NotFound, Internal, etc.) is expected — store is a stub.
		t.Logf("got expected non-auth error: %v", err)
	}
}

func TestServer_PlaySession_AuthAppliesPerStream(t *testing.T) {
	rig := newIntegRig(t)
	ctx := rig.bearerCtx(t, "auth0|stream-user")

	stream, err := rig.client.PlaySession(ctx)
	if err != nil {
		t.Fatalf("PlaySession open: %v", err)
	}

	// Send one command and receive the response (will be a rejection since
	// there's no real game, but the stream opened — auth passed).
	if err := stream.Send(&pb.PlayerCommand{
		GameId: uuid.New().String(),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read back whatever the server sends (rejection event or stream close).
	_, recvErr := stream.Recv()
	if recvErr != nil && recvErr != io.EOF {
		// A gRPC status error here is fine — what we verify is that it's NOT Unauthenticated.
		if s, ok := status.FromError(recvErr); ok && s.Code() == codes.Unauthenticated {
			t.Errorf("stream rejected as Unauthenticated despite valid token")
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// integTestCustomClaims satisfies validator.CustomClaims without extracting
// namespaced fields (not needed for integration auth tests).
type integTestCustomClaims struct{}

func (*integTestCustomClaims) Validate(_ context.Context) error { return nil }

// noopRouter satisfies server's commandSubmitter interface.
type noopRouter struct{}

func (noopRouter) Submit(_ context.Context, _ uuid.UUID, _ string, _ *pb.PlayerCommand) ([]*pb.GameEvent, error) {
	return nil, status.Error(codes.NotFound, "noopRouter: no such game")
}
func (noopRouter) Invalidate(_ uuid.UUID) {}

// integFakeStore is a minimal store for integration tests.
// JoinTable/LeaveTable go through lobby.JoinTable which uses WithTx + various methods.
// We return stub errors rather than implementing the full flow.
type integFakeStore struct {
	mu           sync.Mutex
	byExternalID map[string]store.User
}

func newIntegFakeStore() *integFakeStore {
	return &integFakeStore{byExternalID: make(map[string]store.User)}
}

func (f *integFakeStore) GetUserByExternalID(_ context.Context, extID string) (store.User, store.UserSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byExternalID[extID]
	if !ok {
		return store.User{}, store.UserSnapshot{}, store.ErrNotFound
	}
	return u, store.UserSnapshot{}, nil
}

func (f *integFakeStore) CreateUser(_ context.Context, in store.UserInput) (store.User, store.UserSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.byExternalID[in.ExternalID]; exists {
		return store.User{}, store.UserSnapshot{}, store.ErrDuplicateExternalID
	}
	id := in.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	u := store.User{ID: id, DisplayName: in.DisplayName, ExternalID: in.ExternalID}
	f.byExternalID[in.ExternalID] = u
	return u, store.UserSnapshot{}, nil
}

// WithTx returns NotFound so lobby ops fail fast without panicking.
func (f *integFakeStore) WithTx(_ context.Context, _ func(store.Store) error) error {
	return store.ErrNotFound
}

func (f *integFakeStore) FindEventByCommandID(_ context.Context, _ uuid.UUID) (*pb.GameEvent, error) {
	return nil, store.ErrNotFound
}

// All remaining store.Store methods return zero values / NotFound.
func (f *integFakeStore) CreateGameConfig(_ context.Context, _ *pb.CashGameConfig) (uuid.UUID, error) {
	return uuid.Nil, store.ErrNotFound
}
func (f *integFakeStore) GetGameConfig(_ context.Context, _ uuid.UUID) (*pb.CashGameConfig, error) {
	return nil, store.ErrNotFound
}
func (f *integFakeStore) ListActiveGameConfigs(_ context.Context, _, _ int32) ([]store.GameConfigSummary, error) {
	return nil, nil
}
func (f *integFakeStore) SoftDeleteGameConfig(_ context.Context, _ uuid.UUID) error {
	return store.ErrNotFound
}
func (f *integFakeStore) SearchGameConfigs(_ context.Context, _ store.SearchFilter) ([]store.GameConfigSummary, int64, error) {
	return nil, 0, nil
}
func (f *integFakeStore) CreateTable(_ context.Context, _ *pb.CashGameConfig, _ uuid.UUID) (*pb.GameState, error) {
	return nil, store.ErrNotFound
}
func (f *integFakeStore) AppendEvent(_ context.Context, _ *pb.GameEvent) error { return nil }
func (f *integFakeStore) AppendEvents(_ context.Context, _ []*pb.GameEvent) error {
	return nil
}
func (f *integFakeStore) GetEventsForGame(_ context.Context, _ uuid.UUID, _ uint64, _ int32) ([]*pb.GameEvent, error) {
	return nil, nil
}
func (f *integFakeStore) GetLatestSequence(_ context.Context, _ uuid.UUID) (uint64, error) {
	return 0, nil
}
func (f *integFakeStore) CreateSnapshot(_ context.Context, _ *pb.GameState, _ int64) error {
	return nil
}
func (f *integFakeStore) GetLatestSnapshot(_ context.Context, _ uuid.UUID) (*pb.GameState, uint64, error) {
	return nil, 0, store.ErrNotFound
}
func (f *integFakeStore) GetSnapshotAtOrBefore(_ context.Context, _ uuid.UUID, _ uint64) (*pb.GameState, uint64, error) {
	return nil, 0, store.ErrNotFound
}
func (f *integFakeStore) GetUser(_ context.Context, _ uuid.UUID) (store.User, store.UserSnapshot, error) {
	return store.User{}, store.UserSnapshot{}, store.ErrNotFound
}
func (f *integFakeStore) UpdateUserSettings(_ context.Context, _ store.UpdateUserSettingsInput) (store.User, error) {
	return store.User{}, store.ErrNotFound
}
func (f *integFakeStore) ReportSteps(_ context.Context, _ store.ReportStepsInput) (store.ReportStepsResult, error) {
	return store.ReportStepsResult{}, store.ErrNotFound
}
func (f *integFakeStore) ListDepositReports(_ context.Context, _ uuid.UUID, _, _ int32) ([]store.DepositReport, int64, error) {
	return nil, 0, nil
}
func (f *integFakeStore) FindDepositReport(_ context.Context, _, _ uuid.UUID) (*store.DepositReport, error) {
	return nil, store.ErrNotFound
}
func (f *integFakeStore) DebitUserBalance(_ context.Context, _ uuid.UUID, _ int64) (store.UserSnapshot, error) {
	return store.UserSnapshot{}, store.ErrInsufficientBalance
}
func (f *integFakeStore) CreditUserBalance(_ context.Context, _ uuid.UUID, _ int64) (store.UserSnapshot, error) {
	return store.UserSnapshot{}, nil
}
func (f *integFakeStore) GetUserSnapshot(_ context.Context, _ uuid.UUID) (store.UserSnapshot, error) {
	return store.UserSnapshot{}, store.ErrNotFound
}
