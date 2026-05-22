package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
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
	"github.com/pacepoker/poker/internal/config"
	"github.com/pacepoker/poker/internal/store"
	"github.com/lestrrat-go/jwx/v2/jwa"
	jwxjwk "github.com/lestrrat-go/jwx/v2/jwk"
	jwxjwt "github.com/lestrrat-go/jwx/v2/jwt"
	jose "gopkg.in/go-jose/go-jose.v2"
)

// ── test rig ─────────────────────────────────────────────────────────────────

const (
	testAudience = "https://api.poker.test"
	testNS       = "https://api.poker.test/"
)

type testRig struct {
	key    *rsa.PrivateKey
	server *httptest.Server
	issuer string
	st     *authFakeStore
	auth   *Authenticator
}

// newTestRig generates an RSA key, spins up a fake JWKS httptest server, and
// returns a fully wired Authenticator backed by an in-memory store.
func newTestRig(t *testing.T) *testRig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	// Build JWKS JSON using go-jose (same library as the middleware) so the
	// format is guaranteed compatible when the provider parses it.
	jwkSet := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       &key.PublicKey,
			KeyID:     "test-key-1",
			Algorithm: "RS256",
			Use:       "sig",
		}},
	}
	jwksJSON, err := json.Marshal(jwkSet)
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksJSON)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	issuer := srv.URL + "/"
	jwksURL, _ := url.Parse(srv.URL + "/jwks")
	issuerURL, _ := url.Parse(issuer)

	provider := jwks.NewCachingProvider(issuerURL, 5*time.Minute, jwks.WithCustomJWKSURI(jwksURL))
	v, err := validator.New(
		provider.KeyFunc,
		validator.RS256,
		issuer,
		[]string{testAudience},
		validator.WithCustomClaims(func() validator.CustomClaims {
			return &customClaims{namespace: testNS}
		}),
		validator.WithAllowedClockSkew(30*time.Second),
	)
	if err != nil {
		t.Fatalf("validator.New: %v", err)
	}

	st := newAuthFakeStore()
	cfg := config.Auth0Config{
		Domain:          "test.auth0.local",
		Audience:        testAudience,
		ClaimsNamespace: testNS,
	}
	a := newAuthenticatorWithValidator(cfg, v, st, nil)

	return &testRig{key: key, server: srv, issuer: issuer, st: st, auth: a}
}

// signToken builds and signs a JWT with the rig's RSA key.
// Sensible defaults: sub="auth0|test", 1-hour expiry, correct iss+aud.
func (r *testRig) signToken(t *testing.T, opts ...func(b *jwxjwt.Builder) *jwxjwt.Builder) string {
	t.Helper()
	b := jwxjwt.NewBuilder().
		Subject("auth0|test").
		Issuer(r.issuer).
		Audience([]string{testAudience}).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour))

	for _, opt := range opts {
		b = opt(b)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatalf("build token: %v", err)
	}
	// Build a JWK with the kid set so the JWT header carries kid="test-key-1".
	// go-jose's tryJWKS uses the kid to look up the matching key in the JWKS;
	// without it, it returns the whole set and verification fails.
	privJWK, err := jwxjwk.FromRaw(r.key)
	if err != nil {
		t.Fatalf("jwk.FromRaw: %v", err)
	}
	_ = privJWK.Set(jwxjwk.KeyIDKey, "test-key-1")
	signed, err := jwxjwt.Sign(tok, jwxjwt.WithKey(jwa.RS256, privJWK))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return string(signed)
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestAuthenticator_VerifyValidToken_ReturnsCorrectPrincipal(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|abc123")
	})

	p, err := rig.auth.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.ExternalID != "auth0|abc123" {
		t.Errorf("ExternalID: got %q, want auth0|abc123", p.ExternalID)
	}
	if p.UserID == uuid.Nil {
		t.Error("UserID should not be nil")
	}
}

func TestAuthenticator_VerifyExpiredToken_Rejected(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Expiration(time.Now().Add(-2 * time.Hour))
	})

	_, err := rig.auth.Verify(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestAuthenticator_VerifyWrongAudience_Rejected(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Audience([]string{"https://wrong.audience"})
	})

	_, err := rig.auth.Verify(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for wrong audience, got nil")
	}
}

func TestAuthenticator_VerifyWrongIssuer_Rejected(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Issuer("https://evil.issuer/")
	})

	_, err := rig.auth.Verify(context.Background(), token)
	if err == nil {
		t.Fatal("expected error for wrong issuer, got nil")
	}
}

func TestAuthenticator_VerifyTokenWithCustomClaims_ExtractsDisplayName(t *testing.T) {
	rig := newTestRig(t)
	// Add the namespaced claim via Claim builder.
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|alice").Claim(testNS+"display_name", "Alice")
	})

	p, err := rig.auth.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.DisplayName != "Alice" {
		t.Errorf("DisplayName: got %q, want Alice", p.DisplayName)
	}
}

func TestAuthenticator_VerifyUnknownUser_ProvisionsJIT(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|new-user")
	})

	beforeCount := rig.st.userCount()

	p, err := rig.auth.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	afterCount := rig.st.userCount()
	if afterCount != beforeCount+1 {
		t.Errorf("user count: before=%d after=%d (expected +1)", beforeCount, afterCount)
	}
	if p.UserID == uuid.Nil {
		t.Error("UserID should not be nil after JIT provisioning")
	}
}

func TestAuthenticator_VerifyKnownUser_ReusesRow(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|existing")
	})

	// First call provisions the user.
	p1, err := rig.auth.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("first Verify: %v", err)
	}

	countAfterFirst := rig.st.userCount()

	// Second call must reuse the same row.
	p2, err := rig.auth.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("second Verify: %v", err)
	}

	if rig.st.userCount() != countAfterFirst {
		t.Error("second Verify should not create a new user")
	}
	if p1.UserID != p2.UserID {
		t.Errorf("UserID mismatch: %v vs %v", p1.UserID, p2.UserID)
	}
}

func TestAuthenticator_VerifyConcurrentJITSameSub_SingleUserCreated(t *testing.T) {
	rig := newTestRig(t)
	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|concurrent")
	})

	const n = 20
	ids := make([]uuid.UUID, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			p, err := rig.auth.Verify(context.Background(), token)
			ids[i] = p.UserID
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Verify error: %v", i, err)
		}
	}
	if got := rig.st.userCount(); got != 1 {
		t.Errorf("expected exactly 1 user created, got %d", got)
	}
	first := ids[0]
	for i, id := range ids {
		if id != first {
			t.Errorf("goroutine %d returned different UserID: %v (want %v)", i, id, first)
		}
	}
}

func TestUserCache_HitAvoidsDBLookup(t *testing.T) {
	rig := newTestRig(t)
	cache, err := NewUserCache(100)
	if err != nil {
		t.Fatalf("NewUserCache: %v", err)
	}

	// Re-create authenticator with cache.
	cfg := config.Auth0Config{
		Domain:          "test.auth0.local",
		Audience:        testAudience,
		ClaimsNamespace: testNS,
	}
	// Re-use same validator as rig but attach a cache.
	authWithCache := &Authenticator{
		cfg:       cfg,
		validator: rig.auth.validator,
		users:     rig.st,
		cache:     cache,
	}

	token := rig.signToken(t, func(b *jwxjwt.Builder) *jwxjwt.Builder {
		return b.Subject("auth0|cache-test")
	})

	// First call: cache miss → DB lookup.
	if _, err := authWithCache.Verify(context.Background(), token); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	callsAfterFirst := rig.st.getExternalIDCallCount()

	// Second call: cache hit → no additional DB lookup.
	if _, err := authWithCache.Verify(context.Background(), token); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	callsAfterSecond := rig.st.getExternalIDCallCount()

	if callsAfterSecond != callsAfterFirst {
		t.Errorf("expected 0 additional DB calls on cache hit; GetUserByExternalID called %d extra times",
			callsAfterSecond-callsAfterFirst)
	}
}

// ── fake store ────────────────────────────────────────────────────────────────

type authFakeStore struct {
	mu              sync.Mutex
	byExternalID    map[string]store.User
	getExtIDCalls   int
}

func newAuthFakeStore() *authFakeStore {
	return &authFakeStore{byExternalID: make(map[string]store.User)}
}

func (f *authFakeStore) userCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byExternalID)
}

func (f *authFakeStore) getExternalIDCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getExtIDCalls
}

func (f *authFakeStore) GetUserByExternalID(_ context.Context, extID string) (store.User, store.UserSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getExtIDCalls++
	u, ok := f.byExternalID[extID]
	if !ok {
		return store.User{}, store.UserSnapshot{}, store.ErrNotFound
	}
	return u, store.UserSnapshot{}, nil
}

func (f *authFakeStore) CreateUser(_ context.Context, in store.UserInput) (store.User, store.UserSnapshot, error) {
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

// ── store.Store stubs (auth never calls these) ────────────────────────────────

func (f *authFakeStore) CreateGameConfig(_ context.Context, _ *pb.CashGameConfig) (uuid.UUID, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetGameConfig(_ context.Context, _ uuid.UUID) (*pb.CashGameConfig, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) ListActiveGameConfigs(_ context.Context, _, _ int32) ([]store.GameConfigSummary, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) SoftDeleteGameConfig(_ context.Context, _ uuid.UUID) error {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) SearchGameConfigs(_ context.Context, _ store.SearchFilter) ([]store.GameConfigSummary, int64, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) CreateTable(_ context.Context, _ *pb.CashGameConfig, _ uuid.UUID) (*pb.GameState, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) AppendEvent(_ context.Context, _ *pb.GameEvent) error {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) AppendEvents(_ context.Context, _ []*pb.GameEvent) error {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetEventsForGame(_ context.Context, _ uuid.UUID, _ uint64, _ int32) ([]*pb.GameEvent, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetLatestSequence(_ context.Context, _ uuid.UUID) (uint64, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) FindEventByCommandID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*pb.GameEvent, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) FindEventByCommandIDGlobal(_ context.Context, _ uuid.UUID) (*pb.GameEvent, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) CreateSnapshot(_ context.Context, _ *pb.GameState, _ int64) error {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetLatestSnapshot(_ context.Context, _ uuid.UUID) (*pb.GameState, uint64, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetSnapshotAtOrBefore(_ context.Context, _ uuid.UUID, _ uint64) (*pb.GameState, uint64, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) WithTx(_ context.Context, fn func(store.Store) error) error {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetUser(_ context.Context, _ uuid.UUID) (store.User, store.UserSnapshot, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) UpdateUserSettings(_ context.Context, _ store.UpdateUserSettingsInput) (store.User, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) ReportSteps(_ context.Context, _ store.ReportStepsInput) (store.ReportStepsResult, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) ListDepositReports(_ context.Context, _ uuid.UUID, _, _ int32) ([]store.DepositReport, int64, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) FindDepositReport(_ context.Context, _, _ uuid.UUID) (*store.DepositReport, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) DebitUserBalance(_ context.Context, _ uuid.UUID, _ int64) (store.UserSnapshot, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) CreditUserBalance(_ context.Context, _ uuid.UUID, _ int64) (store.UserSnapshot, error) {
	panic("authFakeStore: not implemented")
}
func (f *authFakeStore) GetUserSnapshot(_ context.Context, _ uuid.UUID) (store.UserSnapshot, error) {
	panic("authFakeStore: not implemented")
}
