package auth

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"time"

	"github.com/auth0/go-jwt-middleware/v2/jwks"
	"github.com/auth0/go-jwt-middleware/v2/validator"
	"github.com/google/uuid"

	"github.com/pacepoker/poker/internal/config"
	"github.com/pacepoker/poker/internal/store"
)

// Principal is the authenticated user attached to every request context.
// Populated by the auth interceptor; read by handlers via FromContext.
type Principal struct {
	UserID      uuid.UUID // internal users.id
	ExternalID  string    // Auth0 sub claim
	DisplayName string    // from namespaced JWT claim, may be empty
	Email       string    // from namespaced JWT claim, may be empty
}

type ctxKey struct{}

// WithPrincipal attaches p to ctx. Used by the interceptor and in tests.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the Principal in ctx, or an error if absent.
// Absent principal in a post-interceptor context is a programming error.
func FromContext(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	if !ok {
		return Principal{}, errors.New("auth: no principal in context")
	}
	return p, nil
}

// Authenticator verifies RS256 JWTs against Auth0's JWKS and resolves
// them to internal Principals (with JIT user provisioning).
// Construct once at startup; safe for concurrent use.
type Authenticator struct {
	cfg       config.Auth0Config
	validator *validator.Validator
	users     store.Store
	cache     *userCache // may be nil
}

// NewAuthenticator wires up the JWKS caching provider and JWT validator.
func NewAuthenticator(cfg config.Auth0Config, users store.Store, cache *userCache) (*Authenticator, error) {
	issuerURL, err := url.Parse("https://" + cfg.Domain + "/")
	if err != nil {
		return nil, fmt.Errorf("parse issuer URL: %w", err)
	}

	provider := jwks.NewCachingProvider(issuerURL, 5*time.Minute)

	v, err := validator.New(
		provider.KeyFunc,
		validator.RS256,
		issuerURL.String(),
		[]string{cfg.Audience},
		validator.WithCustomClaims(func() validator.CustomClaims {
			return &customClaims{namespace: cfg.ClaimsNamespace}
		}),
		validator.WithAllowedClockSkew(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("init validator: %w", err)
	}

	return &Authenticator{
		cfg:       cfg,
		validator: v,
		users:     users,
		cache:     cache,
	}, nil
}

// newAuthenticatorWithValidator is used in same-package tests to inject a
// pre-built validator without going through NewAuthenticator.
func newAuthenticatorWithValidator(cfg config.Auth0Config, v *validator.Validator, users store.Store, cache *userCache) *Authenticator {
	return &Authenticator{cfg: cfg, validator: v, users: users, cache: cache}
}

// NewAuthenticatorForTest creates an Authenticator from a pre-built validator.
// Intended for integration tests in other packages that need a fake JWKS server
// without going through the full OIDC discovery URL construction.
func NewAuthenticatorForTest(cfg config.Auth0Config, v *validator.Validator, users store.Store, cache *userCache) *Authenticator {
	return &Authenticator{cfg: cfg, validator: v, users: users, cache: cache}
}

// Verify validates a raw bearer token and returns the corresponding Principal,
// provisioning a new user row JIT if this is the first request for the sub.
func (a *Authenticator) Verify(ctx context.Context, token string) (Principal, error) {
	parsed, err := a.validator.ValidateToken(ctx, token)
	if err != nil {
		return Principal{}, fmt.Errorf("validate token: %w", err)
	}
	claims, ok := parsed.(*validator.ValidatedClaims)
	if !ok {
		return Principal{}, errors.New("auth: unexpected claims type")
	}

	sub := claims.RegisteredClaims.Subject
	if sub == "" {
		return Principal{}, errors.New("auth: token missing sub claim")
	}

	custom, _ := claims.CustomClaims.(*customClaims)

	userID, err := a.resolveUser(ctx, sub, custom)
	if err != nil {
		return Principal{}, fmt.Errorf("resolve user: %w", err)
	}

	p := Principal{
		UserID:     userID,
		ExternalID: sub,
	}
	if custom != nil {
		p.DisplayName = custom.DisplayName
		p.Email = custom.Email
	}
	return p, nil
}

// resolveUser maps sub → internal UUID, provisioning the user on first sight.
func (a *Authenticator) resolveUser(ctx context.Context, sub string, custom *customClaims) (uuid.UUID, error) {
	if a.cache != nil {
		if id, ok := a.cache.Get(sub); ok {
			return id, nil
		}
	}

	user, _, err := a.users.GetUserByExternalID(ctx, sub)
	switch {
	case err == nil:
		if a.cache != nil {
			a.cache.Put(sub, user.ID)
		}
		return user.ID, nil
	case errors.Is(err, store.ErrNotFound):
		// first time we see this sub — provision below
	default:
		return uuid.Nil, fmt.Errorf("lookup user: %w", err)
	}

	displayName := ""
	if custom != nil {
		displayName = custom.DisplayName
	}
	if displayName == "" {
		displayName = "Player " + shortHash(sub)
	}

	newID := uuid.New()
	created, _, err := a.users.CreateUser(ctx, store.UserInput{
		ID:          newID,
		DisplayName: displayName,
		ExternalID:  sub,
		Timezone:    "UTC",
	})
	if err == nil {
		if a.cache != nil {
			a.cache.Put(sub, created.ID)
		}
		return created.ID, nil
	}

	// Likely a unique-constraint violation on external_id from a concurrent
	// request that beat us here. Re-read and return that row.
	user, _, err2 := a.users.GetUserByExternalID(ctx, sub)
	if err2 != nil {
		return uuid.Nil, fmt.Errorf("provision user (after conflict): %w", err)
	}
	if a.cache != nil {
		a.cache.Put(sub, user.ID)
	}
	return user.ID, nil
}

func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}
