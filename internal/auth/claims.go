package auth

import (
	"context"
	"encoding/json"
)

// customClaims extracts profile fields from the namespaced JWT claims that
// an Auth0 Login Action injects into access tokens.
//
// Auth0 Action example (add in the Auth0 dashboard under Actions → Flows):
//
//	exports.onExecutePostLogin = async (event, api) => {
//	  const ns = "https://api.poker.yourdomain.com/";
//	  api.accessToken.setCustomClaim(ns + "display_name", event.user.name);
//	  api.accessToken.setCustomClaim(ns + "email", event.user.email);
//	};
type customClaims struct {
	// namespace is the URL prefix used for all custom claim names.
	// Set by the factory func passed to validator.WithCustomClaims before
	// UnmarshalJSON is called, so it's already populated at parse time.
	namespace string

	DisplayName string
	Email       string
}

// Validate satisfies validator.CustomClaims. Business-level validation of
// custom claims (e.g. "must have display_name") lives here if needed.
func (c *customClaims) Validate(_ context.Context) error { return nil }

// UnmarshalJSON is called by go-jwt-middleware after it parses the JWT body.
// Namespaced claim names can't be expressed as struct tags, so we extract
// them manually from the raw JSON map.
func (c *customClaims) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw[c.namespace+"display_name"].(string); ok {
		c.DisplayName = v
	}
	if v, ok := raw[c.namespace+"email"].(string); ok {
		c.Email = v
	}
	return nil
}
