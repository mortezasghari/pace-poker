package config

import (
	"errors"
	"os"
	"strings"
)

// Auth0Config holds the Auth0 tenant settings read from environment variables.
type Auth0Config struct {
	// Domain is the Auth0 tenant domain, e.g. "yourtenant.auth0.com" (no scheme).
	Domain string

	// Audience is the API identifier registered in Auth0,
	// e.g. "https://api.poker.yourdomain.com".
	Audience string

	// ClaimsNamespace is the prefix for custom JWT claims injected by an Auth0
	// Action, e.g. "https://api.poker.yourdomain.com/". Must end with "/".
	ClaimsNamespace string

	// DevModeAllowStubAuth enables the x-dev-player-id metadata header as an
	// alternative to a JWT. MUST be false in production.
	DevModeAllowStubAuth bool
}

// LoadAuth0FromEnv reads Auth0 configuration from environment variables.
// Returns an error if the mandatory variables are absent.
//
// Required:
//   - AUTH0_DOMAIN
//   - AUTH0_AUDIENCE
//
// Optional:
//   - AUTH0_CLAIMS_NAMESPACE (defaults to <audience>/)
//   - AUTH_DEV_MODE=1 to enable the stub-auth escape hatch (local dev only)
func LoadAuth0FromEnv() (Auth0Config, error) {
	domain := os.Getenv("AUTH0_DOMAIN")
	audience := os.Getenv("AUTH0_AUDIENCE")
	if domain == "" || audience == "" {
		return Auth0Config{}, errors.New("AUTH0_DOMAIN and AUTH0_AUDIENCE must be set")
	}

	ns := os.Getenv("AUTH0_CLAIMS_NAMESPACE")
	if ns == "" {
		ns = audience + "/"
	}
	if !strings.HasSuffix(ns, "/") {
		ns += "/"
	}

	return Auth0Config{
		Domain:               domain,
		Audience:             audience,
		ClaimsNamespace:      ns,
		DevModeAllowStubAuth: os.Getenv("AUTH_DEV_MODE") == "1",
	}, nil
}
