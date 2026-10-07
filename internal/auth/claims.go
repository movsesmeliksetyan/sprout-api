// Package auth validates Auth0 access tokens and exposes the caller's claims.
package auth

import "context"

// ClaimsNamespace prefixes the custom claims that the tenant's Action adds to
// access tokens (docs/auth0-setup.md). Auth0 drops custom claims that are not
// namespaced.
const ClaimsNamespace = "https://sprout.app/"

// Claims is what the API reads from a verified access token.
type Claims struct {
	// Subject is the Auth0 user id, such as "apple|001234.abcd".
	Subject string `json:"sub"`
	// Email and Name come from the namespaced claims and are empty when the
	// identity provider did not share them.
	Email string `json:"email"`
	Name  string `json:"name"`
}

type claimsKey struct{}

// WithClaims returns a context carrying the caller's verified claims.
func WithClaims(ctx context.Context, claims Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, claims)
}

// ClaimsFromContext returns the claims put there by Middleware. ok is false
// outside an authenticated request.
func ClaimsFromContext(ctx context.Context) (claims Claims, ok bool) {
	claims, ok = ctx.Value(claimsKey{}).(Claims)
	return claims, ok
}

// customClaims are the claims read from the token beyond the registered ones.
type customClaims struct {
	Email string `json:"https://sprout.app/email"`
	Name  string `json:"https://sprout.app/name"`
}

// Validate implements validator.CustomClaims. Both claims are optional.
func (*customClaims) Validate(context.Context) error { return nil }
