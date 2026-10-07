package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/auth0/go-jwt-middleware/v3/core"
	"github.com/auth0/go-jwt-middleware/v3/jwks"
	"github.com/auth0/go-jwt-middleware/v3/validator"

	"github.com/movsesmeliksetyan/sprout-api/internal/httpx"
)

const (
	// defaultKeysTTL is how long the tenant's signing keys are cached. Auth0
	// publishes the next key before it starts signing with it, so a rotation
	// is picked up by the periodic refetch.
	defaultKeysTTL = 15 * time.Minute
	// keysFetchTimeout bounds one request to the tenant, well inside the
	// API's own request deadline.
	keysFetchTimeout = 5 * time.Second
	// clockSkew is the tolerance on exp, nbf and iat.
	clockSkew = 30 * time.Second

	// machineSubjectSuffix ends the subject of a client-credentials token,
	// which stands for an application rather than a person.
	machineSubjectSuffix = "@clients"
)

var (
	// ErrInvalidToken means the token must not be trusted.
	ErrInvalidToken = errors.New("auth: invalid token")
	// ErrKeysUnavailable means the token could not be checked because the
	// tenant's signing keys could not be fetched. It says nothing about the
	// token.
	ErrKeysUnavailable = errors.New("auth: signing keys unavailable")
)

// Verifier checks an access token and returns the claims it carries. The
// error matches ErrKeysUnavailable when the check could not be made, and is
// otherwise a rejection of the token.
type Verifier interface {
	Verify(ctx context.Context, token string) (Claims, error)
}

// JWKSVerifier verifies RS256 access tokens against the issuer's published
// keys.
type JWKSVerifier struct {
	validator *validator.Validator
}

// Option customises a JWKSVerifier.
type Option func(*verifierOptions)

type verifierOptions struct {
	keysTTL time.Duration
}

// WithKeysTTL sets how long the issuer's keys are cached (default 15 min).
func WithKeysTTL(ttl time.Duration) Option {
	return func(o *verifierOptions) { o.keysTTL = ttl }
}

// NewAuth0Verifier returns the verifier for an Auth0 tenant, given its
// hostname and the identifier of the Sprout API configured in it.
func NewAuth0Verifier(domain, audience string, opts ...Option) (*JWKSVerifier, error) {
	return NewVerifier("https://"+domain+"/", audience, opts...)
}

// NewVerifier returns a verifier for tokens issued by issuer for audience.
// The issuer's keys are discovered and fetched on first use, not here.
func NewVerifier(issuer, audience string, opts ...Option) (*JWKSVerifier, error) {
	options := verifierOptions{keysTTL: defaultKeysTTL}
	for _, opt := range opts {
		opt(&options)
	}

	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: issuer: %w", err)
	}
	keys, err := jwks.NewCachingProvider(
		jwks.WithIssuerURL(issuerURL),
		jwks.WithCacheTTL(options.keysTTL),
		jwks.WithCustomClient(&http.Client{Timeout: keysFetchTimeout}),
		jwks.WithStrictJWKSURIOrigin(),
	)
	if err != nil {
		return nil, fmt.Errorf("auth: key provider: %w", err)
	}
	v, err := validator.New(
		validator.WithKeyFunc(keys.KeyFunc),
		validator.WithAlgorithm(validator.RS256),
		validator.WithIssuer(issuer),
		validator.WithAudience(audience),
		validator.WithAllowedClockSkew(clockSkew),
		validator.WithCustomClaims(func() *customClaims { return &customClaims{} }),
	)
	if err != nil {
		return nil, fmt.Errorf("auth: validator: %w", err)
	}
	return &JWKSVerifier{validator: v}, nil
}

// Verify implements Verifier.
func (v *JWKSVerifier) Verify(ctx context.Context, token string) (Claims, error) {
	validated, err := v.validator.ValidateToken(ctx, token)
	if err != nil {
		var failure *core.ValidationError
		if !errors.As(err, &failure) {
			return Claims{}, ErrInvalidToken
		}
		if failure.Code == core.ErrorCodeJWKSFetchFailed {
			return Claims{}, fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
		}
		// Only the code: the library's message can quote parts of the token.
		return Claims{}, fmt.Errorf("%w: %s", ErrInvalidToken, failure.Code)
	}

	claims, ok := validated.(*validator.ValidatedClaims)
	if !ok {
		return Claims{}, fmt.Errorf("%w: unexpected claims type %T", ErrInvalidToken, validated)
	}
	subject := claims.RegisteredClaims.Subject
	switch {
	case subject == "":
		return Claims{}, fmt.Errorf("%w: no subject", ErrInvalidToken)
	case strings.HasSuffix(subject, machineSubjectSuffix):
		return Claims{}, fmt.Errorf("%w: machine token", ErrInvalidToken)
	}

	out := Claims{Subject: subject}
	if custom, ok := claims.CustomClaims.(*customClaims); ok {
		out.Email = custom.Email
		out.Name = custom.Name
	}
	return out, nil
}

// Middleware admits only requests that carry a bearer token the verifier
// accepts, and puts the token's claims in the request context. Every other
// request is answered with 401 unauthenticated, except that a failure to
// reach the signing keys is 503 unavailable: the client should retry, not
// sign the user out.
func Middleware(verifier Verifier, responder *httpx.Responder, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reject := func(reason string) {
				logger.DebugContext(r.Context(), "token rejected", slog.String("reason", reason))
				w.Header().Set("WWW-Authenticate", "Bearer")
				responder.Error(w, r, httpx.ErrUnauthenticated)
			}

			token, ok := bearerToken(r)
			if !ok {
				reject("no bearer token")
				return
			}
			claims, err := verifier.Verify(r.Context(), token)
			switch {
			case errors.Is(err, ErrKeysUnavailable):
				responder.Error(w, r, fmt.Errorf("%w: %w", httpx.ErrUnavailable, err))
				return
			case err != nil:
				reject(err.Error())
				return
			}

			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
		})
	}
}

// bearerToken returns the credentials of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
